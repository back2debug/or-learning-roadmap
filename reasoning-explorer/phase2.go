package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const phase2Model = "deepseek/deepseek-r1-0528" // current R1 checkpoint

func phase2() error {
	// Identical prompt to phase 1 — the model is the only variable.
	req := ChatRequest{
		Model:    phase2Model,
		Messages: []Message{{Role: "user", Content: puzzlePrompt}},
		Usage:    &UsageRequest{Include: true},
	}

	res, err := sendChat("phase2", req)
	if err != nil {
		return err
	}
	p := res.Parsed
	msg := p.Choices[0].Message

	fmt.Println("\n=== WHERE THE REASONING LIVES ===")
	fmt.Printf("message.content   (%4d chars): %q\n", len(msg.Content), truncate(msg.Content, 120))
	fmt.Printf("message.reasoning (%4d chars): %q\n", len(msg.Reasoning), truncate(msg.Reasoning, 300))
	fmt.Printf("message.reasoning_details: %d block(s)\n", len(msg.ReasoningDetails))
	for _, d := range msg.ReasoningDetails {
		fmt.Printf("  [%d] type=%q format=%q id=%q\n", d.Index, d.Type, d.Format, d.ID)
		switch d.Type {
		case "reasoning.text":
			fmt.Printf("      text: %d chars, signature: %q\n", len(d.Text), truncate(d.Signature, 40))
		case "reasoning.summary":
			fmt.Printf("      summary: %d chars\n", len(d.Summary))
		case "reasoning.encrypted":
			fmt.Printf("      data: %d chars (base64, opaque)\n", len(d.Data))
		}
	}

	fmt.Println("\n=== USAGE: PHASE 1 vs PHASE 2 ===")
	printUsageRow := func(label string, u Usage) {
		reason := 0
		if u.CompletionTokensDetails != nil {
			reason = u.CompletionTokensDetails.ReasoningTokens
		}
		fmt.Printf("%-28s prompt=%-4d completion=%-5d reasoning=%-5d visible-answer=%-4d cost=$%.7f\n",
			label, u.PromptTokens, u.CompletionTokens, reason, u.CompletionTokens-reason, u.Cost)
	}
	if base, name := latestUsage("phase1"); base != nil {
		printUsageRow(name, *base)
	} else {
		fmt.Println("(no phase1 response log found — run phase1 first for the comparison row)")
	}
	printUsageRow(phase2Model, p.Usage)

	fmt.Println("\nNote: completion_tokens INCLUDES the reasoning tokens. The visible")
	fmt.Println("answer is completion_tokens - reasoning_tokens. You are billed the")
	fmt.Println("model's OUTPUT rate for all of them — reasoning is priced as output.")
	return nil
}

// latestUsage reads usage from the newest logs/*_<phase>_*_response.json.
func latestUsage(phase string) (*Usage, string) {
	matches, _ := filepath.Glob(filepath.Join("logs", "*_"+phase+"_*_response.json"))
	if len(matches) == 0 {
		return nil, ""
	}
	slices.Sort(matches) // timestamp prefix sorts chronologically
	raw, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		return nil, ""
	}
	var r ChatResponse
	if json.Unmarshal(raw, &r) != nil {
		return nil, ""
	}
	return &r.Usage, r.Model + " (from log)"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
