package main

import "fmt"

// phase5 sends the same prompt with the same reasoning effort across provider
// families and compares what comes back.
func phase5() error {
	models := []string{
		"deepseek/deepseek-r1-0528",
		"anthropic/claude-haiku-4.5",
		"openai/gpt-5-mini",
		"google/gemini-2.5-flash",
		"qwen/qwen3-235b-a22b-thinking-2507",
	}

	type report struct {
		model, provider string
		visibility      string // returned / summarized / encrypted / hidden
		detailShapes    []string
		promptTok       int
		complTok        int
		reasonTok       int
		cost            float64
		latencySec      float64
		failed          bool
	}
	var reports []report

	for _, model := range models {
		req := ChatRequest{
			Model:     model,
			Messages:  []Message{{Role: "user", Content: puzzlePrompt}},
			Reasoning: &ReasoningConfig{Effort: "medium"},
			Usage:     &UsageRequest{Include: true},
		}
		res, err := sendChat("phase5", req)
		if err != nil {
			fmt.Printf("!! %s failed: %v (continuing)\n", model, err)
			reports = append(reports, report{model: model, failed: true})
			continue
		}
		p := res.Parsed
		msg := p.Choices[0].Message

		r := report{
			model:      model,
			provider:   p.Provider,
			promptTok:  p.Usage.PromptTokens,
			complTok:   p.Usage.CompletionTokens,
			cost:       p.Usage.Cost,
			latencySec: res.Latency.Seconds(),
		}
		if p.Usage.CompletionTokensDetails != nil {
			r.reasonTok = p.Usage.CompletionTokensDetails.ReasoningTokens
		}

		// Classify what came back in reasoning_details.
		hasText, hasSummary, hasEncrypted := false, false, false
		for _, d := range msg.ReasoningDetails {
			shape := fmt.Sprintf("type=%s format=%s", d.Type, d.Format)
			switch d.Type {
			case "reasoning.text":
				hasText = true
				shape += fmt.Sprintf(" text=%dch", len(d.Text))
				if d.Signature != "" {
					shape += " SIGNED"
				}
			case "reasoning.summary":
				hasSummary = true
				shape += fmt.Sprintf(" summary=%dch", len(d.Summary))
			case "reasoning.encrypted":
				hasEncrypted = true
				shape += fmt.Sprintf(" data=%dch", len(d.Data))
			}
			r.detailShapes = append(r.detailShapes, shape)
		}
		switch {
		case hasText:
			r.visibility = "raw text"
		case hasSummary:
			r.visibility = "summary"
		case hasEncrypted:
			r.visibility = "encrypted"
		case msg.Reasoning != "":
			r.visibility = "raw text (no details)"
		case r.reasonTok > 0:
			r.visibility = "HIDDEN (billed!)"
		default:
			r.visibility = "none"
		}
		reports = append(reports, r)
	}

	fmt.Println("\n=== PHASE 5: CROSS-PROVIDER COMPARISON (effort=medium, same prompt) ===")
	fmt.Printf("%-36s %-12s %-18s %7s %7s %7s %8s %11s\n",
		"model", "provider", "reasoning vis.", "prompt", "compl", "reason", "latency", "cost")
	for _, r := range reports {
		if r.failed {
			fmt.Printf("%-36s FAILED (see log above)\n", r.model)
			continue
		}
		fmt.Printf("%-36s %-12s %-18s %7d %7d %7d %7.1fs $%.7f\n",
			r.model, r.provider, r.visibility, r.promptTok, r.complTok, r.reasonTok,
			r.latencySec, r.cost)
	}

	fmt.Println("\n=== reasoning_details STRUCTURE PER MODEL ===")
	for _, r := range reports {
		if r.failed {
			continue
		}
		fmt.Printf("%s:\n", r.model)
		if len(r.detailShapes) == 0 {
			fmt.Println("  (no reasoning_details array)")
		}
		for i, s := range r.detailShapes {
			fmt.Printf("  [%d] %s\n", i, s)
		}
	}
	return nil
}
