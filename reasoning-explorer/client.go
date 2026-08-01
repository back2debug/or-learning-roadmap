package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const endpoint = "https://openrouter.ai/api/v1/chat/completions"

// httpClient bounds every request end-to-end; http.DefaultClient has no
// timeout and would hang forever on a stalled connection. Generous because
// reasoning models can be slow and phase4 reads a stream under it.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

// sendResult bundles everything a phase might want to inspect.
type sendResult struct {
	Parsed  *ChatResponse  // typed view
	Raw     map[string]any // untyped view — shows fields the structs missed
	RawBody []byte
	Latency time.Duration
}

// sendChat is the single funnel for all non-streaming phases. It guarantees:
//  1. the request JSON is printed and saved to logs/ BEFORE sending
//  2. the raw response body is saved and printed BEFORE any parsing
func sendChat(phase string, req ChatRequest) (*sendResult, error) {
	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	stamp := time.Now().Format("2006-01-02T15-04-05")
	base := fmt.Sprintf("%s_%s_%s", stamp, phase, sanitize(req.Model))

	fmt.Printf("\n=== REQUEST (%s -> %s) ===\n%s\n", phase, req.Model, body)
	logFile(base+"_request.json", body)

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey())
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	latency := time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Log raw before parsing — even error bodies get saved.
	pretty := prettyJSON(rawBody)
	logFile(base+"_response.json", pretty)
	fmt.Printf("\n=== RAW RESPONSE (HTTP %d, %.2fs) ===\n%s\n", resp.StatusCode, latency.Seconds(), pretty)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from OpenRouter (body above)", resp.StatusCode)
	}

	res := &sendResult{RawBody: rawBody, Latency: latency}
	if err := json.Unmarshal(rawBody, &res.Raw); err != nil {
		return nil, fmt.Errorf("unmarshal to map: %w", err)
	}
	res.Parsed = &ChatResponse{}
	if err := json.Unmarshal(rawBody, res.Parsed); err != nil {
		return nil, fmt.Errorf("unmarshal to struct: %w", err)
	}
	// Every phase indexes Choices[0]; fail here instead of panicking there.
	if len(res.Parsed.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices (body above)")
	}
	return res, nil
}

func apiKey() string {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENROUTER_API_KEY is not set")
		os.Exit(1)
	}
	return key
}

func sanitize(model string) string {
	return strings.NewReplacer("/", "-", ":", "-").Replace(model)
}

func logFile(name string, data []byte) {
	// Logs hold full prompts and completions — keep them owner-only.
	if err := os.MkdirAll("logs", 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "warn: mkdir logs: %v\n", err)
		return
	}
	path := filepath.Join("logs", name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warn: write %s: %v\n", path, err)
		return
	}
	fmt.Printf("  [logged -> %s]\n", path)
}

// prettyJSON indents raw JSON; if that fails (non-JSON body), returns it as-is.
func prettyJSON(raw []byte) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return raw
	}
	return buf.Bytes()
}
