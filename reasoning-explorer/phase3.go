package main

import (
	"fmt"
	"time"
)

// phase3 runs a matrix over the reasoning config: same model, same prompt,
// only the "reasoning" object changes.
func phase3() error {
	truev := true
	matrix := []struct {
		label  string
		config *ReasoningConfig
	}{
		{"effort-low", &ReasoningConfig{Effort: "low"}},
		{"effort-high", &ReasoningConfig{Effort: "high"}},
		{"budget-512", &ReasoningConfig{MaxTokens: 512}},
		{"exclude", &ReasoningConfig{Exclude: &truev}},
		{"default", nil}, // no reasoning param at all
	}

	type row struct {
		label     string
		reasonTok int
		complTok  int
		returned  bool
		latency   time.Duration
		cost      float64
		finish    string
	}
	var rows []row

	for _, m := range matrix {
		req := ChatRequest{
			Model:     phase2Model,
			Messages:  []Message{{Role: "user", Content: puzzlePrompt}},
			Reasoning: m.config,
			Usage:     &UsageRequest{Include: true},
		}
		res, err := sendChat("phase3-"+m.label, req)
		if err != nil {
			// Log and keep going — a rejected config is itself a finding.
			fmt.Printf("!! %s failed: %v (continuing)\n", m.label, err)
			rows = append(rows, row{label: m.label, finish: "ERROR"})
			continue
		}
		p := res.Parsed
		r := row{
			label:    m.label,
			complTok: p.Usage.CompletionTokens,
			latency:  res.Latency,
			cost:     p.Usage.Cost,
			finish:   p.Choices[0].FinishReason,
			returned: p.Choices[0].Message.Reasoning != "" || len(p.Choices[0].Message.ReasoningDetails) > 0,
		}
		if p.Usage.CompletionTokensDetails != nil {
			r.reasonTok = p.Usage.CompletionTokensDetails.ReasoningTokens
		}
		rows = append(rows, r)
	}

	fmt.Println("\n=== PHASE 3 COMPARISON (model:", phase2Model, ") ===")
	fmt.Printf("%-12s | %-9s | %-10s | %-7s | %-14s | %-7s | %-10s | %s\n",
		"config", "reasoning", "completion", "visible", "reasoning text", "latency", "cost", "finish")
	fmt.Println("-------------|-----------|------------|---------|----------------|---------|------------|-------")
	for _, r := range rows {
		if r.finish == "ERROR" {
			fmt.Printf("%-12s | request failed, see log above\n", r.label)
			continue
		}
		fmt.Printf("%-12s | %-9d | %-10d | %-7d | %-14s | %6.1fs | $%.7f | %s\n",
			r.label, r.reasonTok, r.complTok, r.complTok-r.reasonTok,
			map[bool]string{true: "returned", false: "hidden"}[r.returned],
			r.latency.Seconds(), r.cost, r.finish)
	}
	return nil
}
