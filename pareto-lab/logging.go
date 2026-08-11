package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var csvHeader = []string{
	"timestamp", "min_coding_score", "prompt_type", "model_selected",
	"cost", "input_tokens", "output_tokens", "response_time_ms",
	// extras beyond the required columns:
	"ttft_ms", "phase", "session_id", "error",
}

// Loggers fans every result out to the console, results.csv, results.json and
// learning_log.txt. All text destined for disk or screen passes through
// scrub() so the API key can never leak into output (OWASP A01/A09).
type Loggers struct {
	apiKey   string
	csvFile  *os.File
	csvW     *csv.Writer
	narrFile *os.File
	jsonPath string
	results  []Result // results from this run; merged with prior runs on flush
	prior    []json.RawMessage
}

func newLoggers(dir, apiKey string) (*Loggers, error) {
	l := &Loggers{apiKey: apiKey, jsonPath: filepath.Join(dir, "results.json")}

	csvFile, err := os.OpenFile(filepath.Join(dir, "results.csv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening results.csv: %w", err)
	}
	l.csvFile = csvFile
	l.csvW = csv.NewWriter(csvFile)

	info, err := csvFile.Stat()
	if err != nil {
		csvFile.Close()
		return nil, fmt.Errorf("stat results.csv: %w", err)
	}
	if info.Size() == 0 {
		if err := l.csvW.Write(csvHeader); err != nil {
			csvFile.Close()
			return nil, fmt.Errorf("writing CSV header: %w", err)
		}
		l.csvW.Flush()
	}

	narrFile, err := os.OpenFile(filepath.Join(dir, "learning_log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		csvFile.Close()
		return nil, fmt.Errorf("opening learning_log.txt: %w", err)
	}
	l.narrFile = narrFile

	// Keep results from previous runs so results.json stays one valid array.
	if raw, err := os.ReadFile(l.jsonPath); err == nil {
		if jerr := json.Unmarshal(raw, &l.prior); jerr != nil {
			fmt.Fprintf(os.Stderr, "warning: existing results.json is not a valid JSON array (%v); starting fresh\n", jerr)
			l.prior = nil
		}
	} else if !os.IsNotExist(err) {
		narrFile.Close()
		csvFile.Close()
		return nil, fmt.Errorf("reading results.json: %w", err)
	}

	return l, nil
}

// Close flushes and releases all file handles.
func (l *Loggers) Close() {
	l.csvW.Flush()
	if err := l.csvW.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: flushing results.csv: %v\n", l.scrub(err.Error()))
	}
	if err := l.csvFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: closing results.csv: %v\n", l.scrub(err.Error()))
	}
	if err := l.narrFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: closing learning_log.txt: %v\n", l.scrub(err.Error()))
	}
}

// scrub redacts the API key from any outbound text. The key should never
// appear in SDK errors, but defense in depth costs one string replace.
func (l *Loggers) scrub(s string) string {
	return strings.ReplaceAll(s, l.apiKey, "[REDACTED]")
}

// Narrate writes one timestamped line of human-readable commentary to both
// the console and learning_log.txt.
func (l *Loggers) Narrate(format string, args ...any) {
	line := l.scrub(fmt.Sprintf(format, args...))
	stamped := time.Now().Format("15:04:05") + " " + line + "\n"
	fmt.Print(stamped)
	if _, err := l.narrFile.WriteString(stamped); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing learning_log.txt: %v\n", l.scrub(err.Error()))
	}
}

// Note writes one line of commentary without a timestamp to both the console
// and learning_log.txt. Used for the multi-line findings summary, where a
// timestamp on every line would drown the prose.
func (l *Loggers) Note(format string, args ...any) {
	line := strings.TrimRight(l.scrub(fmt.Sprintf(format, args...)), " ") + "\n"
	fmt.Print(line)
	if _, err := l.narrFile.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing learning_log.txt: %v\n", l.scrub(err.Error()))
	}
}

// Record prints one result line to the console and persists the result to
// results.csv and results.json.
func (l *Loggers) Record(res Result) {
	res.Err = l.scrub(res.Err)
	res.ResponseText = l.scrub(res.ResponseText)
	res.Request = json.RawMessage(l.scrub(string(res.Request)))

	fmt.Println("  " + l.consoleLine(res))

	scoreStr := ""
	if res.MinCodingScore != nil {
		scoreStr = strconv.FormatFloat(*res.MinCodingScore, 'f', 2, 64)
	}
	costStr := ""
	if res.CostKnown {
		costStr = strconv.FormatFloat(res.Cost, 'f', 8, 64)
	}
	row := []string{
		res.Timestamp.Format(time.RFC3339),
		scoreStr,
		res.PromptType,
		res.ModelSelected,
		costStr,
		strconv.FormatInt(res.InputTokens, 10),
		strconv.FormatInt(res.OutputTokens, 10),
		strconv.FormatInt(res.TotalMs, 10),
		strconv.FormatInt(res.TTFTMs, 10),
		res.Phase,
		res.SessionID,
		res.Err,
	}
	if err := l.csvW.Write(row); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing results.csv row: %v\n", l.scrub(err.Error()))
	}
	l.csvW.Flush()
	if err := l.csvW.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: flushing results.csv: %v\n", l.scrub(err.Error()))
	}

	l.results = append(l.results, res)
	if err := l.flushJSON(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing results.json: %v\n", l.scrub(err.Error()))
	}
}

func (l *Loggers) consoleLine(res Result) string {
	score := "default"
	if res.MinCodingScore != nil {
		score = strconv.FormatFloat(*res.MinCodingScore, 'f', 1, 64)
	}
	if res.Err != "" {
		return fmt.Sprintf("Min Score: %s | Prompt: %s | ERROR: %s", score, res.PromptType, res.Err)
	}
	cost := "n/a"
	if res.CostKnown {
		cost = fmt.Sprintf("$%.6f", res.Cost)
	}
	return fmt.Sprintf("Min Score: %s | Prompt: %s | Model: %s | Cost: %s | Tokens: %d/%d | TTFT: %dms | Total: %.2fs",
		score, res.PromptType, res.ModelSelected, cost,
		res.InputTokens, res.OutputTokens, res.TTFTMs, float64(res.TotalMs)/1000.0)
}

// flushJSON rewrites results.json as a single valid array containing prior
// runs plus every result recorded so far, via temp file + rename so a crash
// mid-write can't corrupt the file.
func (l *Loggers) flushJSON() error {
	merged := make([]json.RawMessage, 0, len(l.prior)+len(l.results))
	merged = append(merged, l.prior...)
	for _, r := range l.results {
		b, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshaling result: %w", err)
		}
		merged = append(merged, b)
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling results array: %w", err)
	}

	tmp := l.jsonPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmp, l.jsonPath); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}
