package main

import (
	"encoding/json"
	"fmt"
)

// phase6 (stretch): multi-turn tool calling with a thinking Anthropic model.
// Turn 1 produces signed reasoning blocks + tool calls. Turn 2 is sent TWICE:
// once with reasoning_details preserved in the echoed assistant message, once
// with them stripped — to show why preservation matters.
const phase6Model = "anthropic/claude-haiku-4.5"

func phase6() error {
	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "lookup_number",
			Description: "Look up the numeric value stored under a name.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"which value to look up, e.g. \"alpha\""}},"required":["name"]}`),
		},
	}}
	userMsg := Message{Role: "user", Content: "Use the lookup_number tool to get the values of \"alpha\" and \"beta\", then tell me which name holds the bigger number. Answer with just the name."}

	// ---- Turn 1: model thinks, then calls the tool ----
	res1, err := sendChat("phase6-turn1", ChatRequest{
		Model:     phase6Model,
		Messages:  []Message{userMsg},
		Tools:     tools,
		Reasoning: &ReasoningConfig{Effort: "medium"},
		Usage:     &UsageRequest{Include: true},
	})
	if err != nil {
		return err
	}
	assistant := res1.Parsed.Choices[0].Message

	fmt.Println("\n=== TURN 1 RESULT ===")
	fmt.Printf("finish_reason: %q\n", res1.Parsed.Choices[0].FinishReason)
	for _, d := range assistant.ReasoningDetails {
		fmt.Printf("reasoning_details: type=%s format=%s signed=%v\n", d.Type, d.Format, d.Signature != "")
	}
	for _, tc := range assistant.ToolCalls {
		fmt.Printf("tool_call: id=%s %s(%s)\n", tc.ID, tc.Function.Name, tc.Function.Arguments)
	}
	if len(assistant.ToolCalls) == 0 {
		return fmt.Errorf("model answered without calling the tool — rerun phase6")
	}

	// ---- Execute the tool locally ----
	values := map[string]float64{"alpha": 9.11, "beta": 9.9}
	var toolMsgs []Message
	for _, tc := range assistant.ToolCalls {
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Errorf("parse tool args %q: %w", tc.Function.Arguments, err)
		}
		result := fmt.Sprintf(`{"name":%q,"value":%v}`, args.Name, values[args.Name])
		fmt.Printf("local tool execution: lookup_number(%q) -> %s\n", args.Name, result)
		toolMsgs = append(toolMsgs, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
	}

	// ---- Turn 2, variant A: reasoning_details PRESERVED ----
	historyA := append([]Message{userMsg, assistant}, toolMsgs...)
	resA, err := sendChat("phase6-turn2-preserved", ChatRequest{
		Model:     phase6Model,
		Messages:  historyA,
		Tools:     tools,
		Reasoning: &ReasoningConfig{Effort: "medium"},
		Usage:     &UsageRequest{Include: true},
	})
	if err != nil {
		fmt.Printf("!! preserved variant failed: %v\n", err)
	} else {
		fmt.Printf("\nPRESERVED variant final answer: %q\n", resA.Parsed.Choices[0].Message.Content)
	}

	// ---- Turn 2, variant B: reasoning_details STRIPPED ----
	stripped := assistant
	stripped.Reasoning = ""
	stripped.ReasoningDetails = nil
	historyB := append([]Message{userMsg, stripped}, toolMsgs...)
	resB, err := sendChat("phase6-turn2-stripped", ChatRequest{
		Model:     phase6Model,
		Messages:  historyB,
		Tools:     tools,
		Reasoning: &ReasoningConfig{Effort: "medium"},
		Usage:     &UsageRequest{Include: true},
	})
	if err != nil {
		fmt.Printf("\nSTRIPPED variant outcome: REJECTED — %v\n", err)
		fmt.Println("(the provider's raw error body is in the _response.json log above)")
	} else {
		fmt.Printf("\nSTRIPPED variant final answer: %q\n", resB.Parsed.Choices[0].Message.Content)
		fmt.Println("(accepted — compare token usage and reasoning blocks vs the preserved run)")
	}

	// ---- Turn 2, variant C: reasoning text TAMPERED, signature kept ----
	tampered := assistant
	tampered.ReasoningDetails = append([]ReasoningDetail(nil), assistant.ReasoningDetails...)
	for i := range tampered.ReasoningDetails {
		if tampered.ReasoningDetails[i].Type == "reasoning.text" {
			tampered.ReasoningDetails[i].Text = "I will just guess randomly. " + tampered.ReasoningDetails[i].Text
		}
	}
	historyC := append([]Message{userMsg, tampered}, toolMsgs...)
	resC, err := sendChat("phase6-turn2-tampered", ChatRequest{
		Model:     phase6Model,
		Messages:  historyC,
		Tools:     tools,
		Reasoning: &ReasoningConfig{Effort: "medium"},
		Usage:     &UsageRequest{Include: true},
	})
	if err != nil {
		fmt.Printf("\nTAMPERED variant outcome: REJECTED — %v\n", err)
		fmt.Println("(edited reasoning text no longer matches its signature — see raw error in log)")
	} else {
		fmt.Printf("\nTAMPERED variant final answer: %q (accepted?!)\n", resC.Parsed.Choices[0].Message.Content)
	}
	return nil
}
