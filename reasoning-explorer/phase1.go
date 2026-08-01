package main

import (
	"fmt"
	"maps"
	"slices"
)

// The same prompt is reused in phase 2 so the only variable is the model.
const puzzlePrompt = "Which number is bigger, 9.11 or 9.9? Answer with just the number."

const phase1Model = "openai/gpt-4o-mini" // cheap, no reasoning support

func phase1() error {
	req := ChatRequest{
		Model:    phase1Model,
		Messages: []Message{{Role: "user", Content: puzzlePrompt}},
		Usage:    &UsageRequest{Include: true},
	}

	res, err := sendChat("phase1", req)
	if err != nil {
		return err
	}

	fmt.Println("\n=== ANNOTATED WALKTHROUGH ===")
	p := res.Parsed
	msg := p.Choices[0].Message

	fmt.Printf(`
id:        %q — OpenRouter generation id ("gen-..."). Vanilla OpenAI uses
           "chatcmpl-...". You can query this id at /api/v1/generation for
           full cost/latency metadata after the fact.
provider:  %q — OpenRouter addition. Which upstream actually served the
           request (OpenRouter routes one model across many providers).
model:     %q — the model that ran (may differ from what you asked for if
           routing/fallbacks kicked in).

choices[0].message:
  role:    %q
  content: %q
  (no "reasoning" / "reasoning_details" — this model doesn't reason)

choices[0].finish_reason:        %q — normalized by OpenRouter:
           "stop" = model finished naturally; "length" = hit max_tokens;
           "tool_calls" / "content_filter" / "error" otherwise.
choices[0].native_finish_reason: %q — OpenRouter addition: the provider's
           raw value before normalization.

usage:
  prompt_tokens:     %d  (your input, tokenized by the provider)
  completion_tokens: %d  (everything generated — this is what you pay output rate on)
  total_tokens:      %d
  cost:              $%.7f — OpenRouter addition (because we sent "usage": {"include": true})
`,
		p.ID, p.Provider, p.Model,
		msg.Role, msg.Content,
		p.Choices[0].FinishReason, p.Choices[0].NativeFinishReason,
		p.Usage.PromptTokens, p.Usage.CompletionTokens, p.Usage.TotalTokens, p.Usage.Cost)

	fmt.Println("Top-level keys present in the raw JSON (vs. what the structs capture):")
	for _, k := range slices.Sorted(maps.Keys(res.Raw)) {
		fmt.Printf("  - %s\n", k)
	}
	fmt.Println("\nCompare that list against ChatResponse in types.go — anything not")
	fmt.Println("listed there was only visible because we also unmarshalled into a map.")
	return nil
}
