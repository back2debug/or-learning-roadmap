// pareto-lab is a learning tool for exploring OpenRouter's Pareto Code router
// (openrouter/pareto-code). It sweeps min_coding_score values across simple and
// complex coding prompts, records which concrete model the router selects, and
// exercises session_id-based sticky routing.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
)

const (
	paretoModel = "openrouter/pareto-code"

	// Test configuration (hardcoded by design).
	iterations = 1 // full test passes per run
	// Completion-token cap per request, bounding cost per run. Reasoning
	// models spend output tokens on hidden thinking before any visible text,
	// so this needs headroom: 400 was fully consumed by reasoning alone on
	// the complex prompt.
	maxTokens = 1600
	outDir    = "." // where results.csv / results.json / learning_log.txt go
)

// scoreSweep holds the min_coding_score values under test, ascending so the
// narrative reads "cheapest tier first, best tier last".
var scoreSweep = []float64{0.3, 0.6, 0.8, 1.0}

// prompts maps prompt type to content. Both are fixed strings — no user input
// flows into requests (OWASP A03) — but validatePrompt still guards them so
// the check is in place if prompts ever become configurable.
var prompts = map[string]string{
	"simple": "Write a Go function that reverses a string, preserving Unicode " +
		"characters. Return only the code with a one-line doc comment.",
	"complex": "Design a concurrency-safe LRU cache in Go using generics, with " +
		"per-entry TTL expiry and O(1) get/put. Sketch the data structures, " +
		"explain the locking strategy and its trade-offs versus sharding, and " +
		"then implement Get and Put.",
}

func loadAPIKey() (string, error) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		return "", errors.New("OPENROUTER_API_KEY is not set")
	}
	// OWASP A07: fail fast on a malformed key instead of sending it upstream.
	// The key value itself is never echoed in this error or anywhere else.
	if !strings.HasPrefix(key, "sk-or-") {
		return "", errors.New("OPENROUTER_API_KEY does not look like an OpenRouter key (expected sk-or-... prefix); key value withheld from output")
	}
	return key, nil
}

func validatePrompt(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("prompt is empty")
	}
	if len(p) > 8000 {
		return fmt.Errorf("prompt is %d bytes, exceeding the 8000-byte cap", len(p))
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	apiKey, err := loadAPIKey()
	if err != nil {
		return err
	}

	for name, p := range prompts {
		if err := validatePrompt(p); err != nil {
			return fmt.Errorf("prompt %q invalid: %w", name, err)
		}
	}

	logs, err := newLoggers(outDir, apiKey)
	if err != nil {
		return err
	}
	defer logs.Close()

	// The SDK talks to https://openrouter.ai/api/v1 by default (OWASP A02:
	// TLS only). We deliberately do not override the server URL.
	client := openrouter.New(openrouter.WithSecurity(apiKey))
	runner := &Runner{client: client, maxTokens: maxTokens}

	logs.Narrate("=== Pareto router lab starting: %d iteration(s), max_tokens=%d ===", iterations, maxTokens)
	logs.Narrate("Sweeping min_coding_score %v over prompt types [simple, complex], then testing sticky sessions.", scoreSweep)

	ctx := context.Background()
	for iter := 1; iter <= iterations; iter++ {
		logs.Narrate("--- Iteration %d of %d ---", iter, iterations)
		runScoreSweep(ctx, runner, logs, iter)
		runStickySession(ctx, runner, logs, iter)
	}

	logs.Summarize()
	logs.Narrate("=== Run complete. See results.csv, results.json and learning_log.txt ===")
	return nil
}

// runScoreSweep sends every prompt type at every min_coding_score value.
// Requests run sequentially on purpose: the point of this tool is watching
// routing decisions unfold one at a time, so the console narrative stays
// aligned with the requests. (A concurrent variant with a WaitGroup would
// finish faster but interleave the story.)
func runScoreSweep(ctx context.Context, r *Runner, logs *Loggers, iter int) {
	logs.Narrate("Phase 1: score sweep. Watching which concrete model the router picks per tier.")

	// Fixed order — Go map iteration order is randomized.
	for _, promptType := range []string{"simple", "complex"} {
		prompt := prompts[promptType]
		for _, score := range scoreSweep {
			score := score
			res := r.Run(ctx, RequestSpec{
				Iteration:      iter,
				Phase:          "score-sweep",
				PromptType:     promptType,
				Prompt:         prompt,
				MinCodingScore: &score,
			})
			logs.Record(res)

			if res.Err != "" {
				logs.Narrate("  min_coding_score=%.1f %s prompt FAILED: %s — continuing with the rest of the sweep.", score, promptType, res.Err)
				continue
			}
			note := ""
			if res.ResponseText == "" && res.ReasoningChars > 0 {
				note = " NOTE: the whole token budget went to hidden reasoning — no visible answer."
			}
			logs.Narrate("  min_coding_score=%.1f routed the %s prompt to %s (tiers: >=0.66 high, >=0.33 medium, <0.33 low).%s",
				score, promptType, res.ModelSelected, note)
		}
	}
}

// runStickySession tests session_id stickiness: a follow-up request reuses the
// session of the first, and a control request omits session_id entirely. If
// stickiness works, the follow-up hits the same model, while the control is
// free to differ (it often lands on the same model anyway when that model is
// still the cheapest in tier — a nuance worth observing).
func runStickySession(ctx context.Context, r *Runner, logs *Loggers, iter int) {
	logs.Narrate("Phase 2: sticky sessions. The same session_id should pin follow-ups to the first model chosen.")

	score := 0.6
	sessionID := fmt.Sprintf("pareto-lab-%d-%d", iter, time.Now().UnixNano())
	logs.Narrate("  Using session_id=%s at min_coding_score=%.1f.", sessionID, score)

	first := r.Run(ctx, RequestSpec{
		Iteration:      iter,
		Phase:          "sticky-first",
		PromptType:     "simple",
		Prompt:         prompts["simple"],
		MinCodingScore: &score,
		SessionID:      sessionID,
	})
	logs.Record(first)
	if first.Err != "" {
		logs.Narrate("  First sticky request failed (%s); skipping the rest of the sticky test this iteration.", first.Err)
		return
	}
	logs.Narrate("  Session opened on %s. Sending a follow-up on the same session...", first.ModelSelected)

	followUp := r.Run(ctx, RequestSpec{
		Iteration:      iter,
		Phase:          "sticky-follow-up",
		PromptType:     "follow-up",
		Prompt:         "Now add proper error handling to that function and explain what can fail.",
		History:        []Exchange{{User: prompts["simple"], Assistant: first.ResponseText}},
		MinCodingScore: &score,
		SessionID:      sessionID,
	})
	logs.Record(followUp)

	control := r.Run(ctx, RequestSpec{
		Iteration:      iter,
		Phase:          "sticky-control",
		PromptType:     "simple",
		Prompt:         prompts["simple"],
		MinCodingScore: &score,
		// No SessionID: this request is routed fresh.
	})
	logs.Record(control)

	switch {
	case followUp.Err != "":
		logs.Narrate("  Follow-up failed (%s); sticky comparison inconclusive.", followUp.Err)
	case followUp.ModelSelected == first.ModelSelected:
		logs.Narrate("  STICKY CONFIRMED: follow-up stayed on %s.", first.ModelSelected)
	default:
		logs.Narrate("  STICKY BROKE: follow-up moved from %s to %s (the first model may have become unavailable).",
			first.ModelSelected, followUp.ModelSelected)
	}
	if control.Err == "" {
		if control.ModelSelected == first.ModelSelected {
			logs.Narrate("  Control (no session_id) also chose %s — expected when that model is still the tier's cheapest; not evidence against stickiness.", control.ModelSelected)
		} else {
			logs.Narrate("  Control (no session_id) chose %s instead — fresh routing diverged from the pinned session.", control.ModelSelected)
		}
	}
}
