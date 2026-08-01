package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Streaming chunk shapes. Same message vocabulary as non-streaming, but the
// payload arrives under "delta" instead of "message".
type StreamChunk struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Choices  []StreamChoice `json:"choices"`
	Usage    *Usage         `json:"usage"` // present only on the final accounting chunk
}

type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"` // role/content/reasoning/reasoning_details fragments
	FinishReason *string `json:"finish_reason"`
}

func phase4() error {
	req := ChatRequest{
		Model:    phase2Model,
		Messages: []Message{{Role: "user", Content: puzzlePrompt}},
		Stream:   true,
		Usage:    &UsageRequest{Include: true}, // ask for the final usage chunk
	}

	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	stamp := time.Now().Format("2006-01-02T15-04-05")
	base := fmt.Sprintf("%s_phase4_%s", stamp, sanitize(req.Model))

	fmt.Printf("\n=== REQUEST (phase4 -> %s) ===\n%s\n", req.Model, body)
	logFile(base+"_request.json", body)

	httpReq, err := http.NewRequest("POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey())
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Raw SSE transcript, written line-by-line as it arrives so nothing is
	// lost even if the stream dies mid-flight.
	if err := os.MkdirAll("logs", 0o700); err != nil {
		return err
	}
	ssePath := filepath.Join("logs", base+"_sse.txt")
	sseFile, err := os.OpenFile(ssePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer sseFile.Close()
	fmt.Printf("  [raw SSE transcript -> %s]\n", ssePath)

	if resp.StatusCode != http.StatusOK {
		// Error bodies aren't SSE — dump them verbatim to both sinks.
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			fmt.Fprintln(sseFile, sc.Text())
			fmt.Println(sc.Text())
		}
		return fmt.Errorf("HTTP %d from OpenRouter", resp.StatusCode)
	}

	fmt.Println("\n=== STREAM (each SSE line, labeled) ===")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		chunkNum                         int
		reasoningChunks, contentChunks   int
		reasoningText, contentText       strings.Builder
		sawTransition                    bool
		firstReasoningAt, firstContentAt time.Duration
		finalUsage                       *Usage
	)

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(sseFile, line) // raw transcript, verbatim

		switch {
		case line == "":
			continue // blank line = SSE event separator
		case strings.HasPrefix(line, ":"):
			// SSE comment — OpenRouter sends ": OPENROUTER PROCESSING" as keepalive
			fmt.Printf("      COMMENT   %s\n", line)
			continue
		case !strings.HasPrefix(line, "data: "):
			fmt.Printf("      UNKNOWN   %q\n", line)
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			fmt.Printf("      DONE      [DONE] sentinel — stream complete\n")
			break
		}

		chunkNum++
		var chunk StreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			fmt.Printf("#%03d  PARSE-ERR %v — raw: %s\n", chunkNum, err, truncate(payload, 120))
			continue
		}

		if chunk.Usage != nil {
			finalUsage = chunk.Usage
			fmt.Printf("#%03d  USAGE     prompt=%d completion=%d reasoning=%d cost=$%.7f\n",
				chunkNum, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens,
				reasoningTokens(chunk.Usage), chunk.Usage.Cost)
		}
		for _, c := range chunk.Choices {
			d := c.Delta
			if d.Role != "" && d.Content == "" && d.Reasoning == "" {
				fmt.Printf("#%03d  ROLE      %q (first chunk announces the role)\n", chunkNum, d.Role)
			}
			if d.Reasoning != "" {
				reasoningChunks++
				reasoningText.WriteString(d.Reasoning)
				if firstReasoningAt == 0 {
					firstReasoningAt = time.Since(start)
				}
				fmt.Printf("#%03d  REASONING %q  (+%d reasoning_details blocks)\n",
					chunkNum, truncate(d.Reasoning, 70), len(d.ReasoningDetails))
			}
			if d.Content != "" {
				if !sawTransition {
					sawTransition = true
					firstContentAt = time.Since(start)
					fmt.Println("      ---------- REASONING -> CONTENT TRANSITION ----------")
				}
				contentChunks++
				contentText.WriteString(d.Content)
				fmt.Printf("#%03d  CONTENT   %q\n", chunkNum, truncate(d.Content, 70))
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				fmt.Printf("#%03d  FINISH    finish_reason=%q\n", chunkNum, *c.FinishReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stream read: %w", err)
	}

	fmt.Println("\n=== STREAM SUMMARY ===")
	fmt.Printf("total chunks:      %d\n", chunkNum)
	fmt.Printf("reasoning chunks:  %d (%d chars), first at %.2fs\n",
		reasoningChunks, reasoningText.Len(), firstReasoningAt.Seconds())
	fmt.Printf("content chunks:    %d (%d chars), first at %.2fs\n",
		contentChunks, contentText.Len(), firstContentAt.Seconds())
	fmt.Printf("total wall time:   %.2fs\n", time.Since(start).Seconds())
	if finalUsage != nil {
		fmt.Printf("final usage chunk: completion=%d reasoning=%d\n",
			finalUsage.CompletionTokens, reasoningTokens(finalUsage))
	}
	fmt.Printf("assembled answer:  %s\n", truncate(contentText.String(), 200))
	return nil
}

func reasoningTokens(u *Usage) int {
	if u.CompletionTokensDetails == nil {
		return 0
	}
	return u.CompletionTokensDetails.ReasoningTokens
}
