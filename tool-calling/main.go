// Command tool-calling progressively explores OpenRouter tool calling in Go:
// Parts 1-3 pin Claude Sonnet 5 explicitly, Parts 4-6 repeat the same
// experiments with OpenRouter's Auto Router (openrouter/auto), Parts 7-9 use
// the Auto Router Beta (openrouter/auto-beta), and a comparison section
// contrasts all three.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// partConfig describes one experiment.
type partConfig struct {
	Title        string
	Model        ModelName // empty → model routing
	Prompt       string
	Tools        []Tool
	ExecuteTools bool // false: single call, inspect only (Parts 1 & 4)
}

// partSummary aggregates what happened in one part, for the comparison.
type partSummary struct {
	Title            string
	RequestedModel   string
	ModelsSeen       []string
	Providers        []string
	Requests         int
	PromptTokens     int
	CompletionTokens int
	ToolCallsMade    int
	FinalAnswer      string
}

func main() {
	logFile, logPath, err := openLogFile()
	if err != nil {
		newLogger(os.Stderr).Error("cannot create log file", "error", err)
		os.Exit(1)
	}
	defer func() { _ = logFile.Close() }()

	// Tee everything: display output → stdout + file, slog events → stderr
	// + file, so ./tool-calling.log holds the complete run transcript.
	output = io.MultiWriter(os.Stdout, logFile)
	logger := newLogger(io.MultiWriter(os.Stderr, logFile))
	logger.Info("writing run transcript", "path", logPath)

	if err := run(logger); err != nil {
		logger.Error("run failed", "error", err)
		os.Exit(1)
	}
}

// run wires up the client and executes all six parts plus the comparison.
// Keeping main() minimal makes the flow testable and the exit path single.
func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := NewClient(logger)

	parts := []partConfig{
		{
			Title:  "PART 1: SIMPLE TOOL CALL (explicit model: Claude Sonnet 5)",
			Model:  ModelClaudeSonnet5,
			Prompt: "What is 47 + 23?",
			Tools:  []Tool{addNumbersTool()},
		},
		{
			Title:        "PART 2: TOOL CALL LOOP (explicit model: Claude Sonnet 5)",
			Model:        ModelClaudeSonnet5,
			Prompt:       "What is 47 + 23?",
			Tools:        []Tool{addNumbersTool()},
			ExecuteTools: true,
		},
		{
			Title:        "PART 3: MULTIPLE TOOLS (explicit model: Claude Sonnet 5)",
			Model:        ModelClaudeSonnet5,
			Prompt:       "What is 7 * 6, and then add 8?",
			Tools:        []Tool{multiplyNumbersTool(), addNumbersTool()},
			ExecuteTools: true,
		},
		{
			Title:  "PART 4: SIMPLE TOOL CALL (model routing: openrouter/auto)",
			Model:  ModelRouted,
			Prompt: "What is 47 + 23?",
			Tools:  []Tool{addNumbersTool()},
		},
		{
			Title:        "PART 5: TOOL CALL LOOP (model routing: openrouter/auto)",
			Model:        ModelRouted,
			Prompt:       "What is 47 + 23?",
			Tools:        []Tool{addNumbersTool()},
			ExecuteTools: true,
		},
		{
			Title:        "PART 6: MULTIPLE TOOLS (model routing: openrouter/auto)",
			Model:        ModelRouted,
			Prompt:       "What is 7 * 6, and then add 8?",
			Tools:        []Tool{multiplyNumbersTool(), addNumbersTool()},
			ExecuteTools: true,
		},
		{
			Title:  "PART 7: SIMPLE TOOL CALL (model routing: openrouter/auto-beta)",
			Model:  ModelRoutedBeta,
			Prompt: "What is 47 + 23?",
			Tools:  []Tool{addNumbersTool()},
		},
		{
			Title:        "PART 8: TOOL CALL LOOP (model routing: openrouter/auto-beta)",
			Model:        ModelRoutedBeta,
			Prompt:       "What is 47 + 23?",
			Tools:        []Tool{addNumbersTool()},
			ExecuteTools: true,
		},
		{
			Title:        "PART 9: MULTIPLE TOOLS (model routing: openrouter/auto-beta)",
			Model:        ModelRoutedBeta,
			Prompt:       "What is 7 * 6, and then add 8?",
			Tools:        []Tool{multiplyNumbersTool(), addNumbersTool()},
			ExecuteTools: true,
		},
	}

	summaries := make([]partSummary, 0, len(parts))
	for _, part := range parts {
		printSection(part.Title)
		summary, err := runPart(ctx, client, logger, part)
		if err != nil {
			return fmt.Errorf("%s: %w", part.Title, err)
		}
		summaries = append(summaries, summary)
	}

	printComparison(summaries)
	return nil
}

// runPart drives one experiment: send request(s), print request/response
// JSON, inspect the parsed structs, and (optionally) execute tool calls in a
// loop until the model produces a final text answer.
func runPart(ctx context.Context, client *Client, logger *slog.Logger, cfg partConfig) (partSummary, error) {
	summary := partSummary{Title: cfg.Title, RequestedModel: requestedModelLabel(cfg.Model)}
	messages := []Message{{Role: roleUser, Content: cfg.Prompt}}

	for turn := 1; turn <= maxConversation; turn++ {
		request := &ChatRequest{
			Model:      string(cfg.Model),
			Messages:   messages,
			Tools:      cfg.Tools,
			ToolChoice: string(ToolChoiceAuto),
		}
		logger.Info("sending request", "turn", turn, "model", summary.RequestedModel,
			"messages", len(messages), "api_key", client.RedactedKey())
		printRequestJSON(request)

		response, raw, err := client.CreateChatCompletion(ctx, request)
		if err != nil {
			return summary, err
		}
		summary.record(response)

		fmt.Fprintf(output, "\n--- Response JSON (turn %d) ---\n%s\n", turn, prettyJSON(raw))
		inspectResponse(response)

		choice := response.Choices[0]
		if choice.FinishReason != finishReasonToolCalls {
			summary.FinalAnswer = choice.Message.Content
			explainOutcome(choice, false)
			return summary, nil
		}
		if !cfg.ExecuteTools {
			explainOutcome(choice, true)
			summary.FinalAnswer = "(tool call requested; not executed in this part)"
			return summary, nil
		}

		// Echo the assistant turn (with its tool_calls) back, then answer
		// every tool call in order — the API requires one tool message per
		// tool_call_id before the conversation can continue.
		messages = append(messages, choice.Message)
		for _, toolCall := range choice.Message.ToolCalls {
			summary.ToolCallsMade++
			result, err := executeTool(toolCall.Function.Name, toolCall.Function.Arguments)
			if err != nil {
				logger.Error("tool execution failed", "tool", toolCall.Function.Name, "error", err)
				result = fmt.Sprintf("error: %v", err)
			}
			logger.Info("executed tool", "tool", toolCall.Function.Name,
				"arguments", toolCall.Function.Arguments, "result", result)
			messages = append(messages, Message{
				Role:       roleTool,
				ToolCallID: toolCall.ID,
				Content:    result,
			})
		}
	}
	return summary, fmt.Errorf("conversation did not converge within %d turns", maxConversation)
}

// record accumulates per-request stats into the part summary.
func (s *partSummary) record(response *ChatResponse) {
	s.Requests++
	s.PromptTokens += response.Usage.PromptTokens
	s.CompletionTokens += response.Usage.CompletionTokens
	s.ModelsSeen = appendUnique(s.ModelsSeen, response.Model)
	s.Providers = appendUnique(s.Providers, response.Provider)
}

// printRequestJSON pretty-prints the outgoing body. The body never contains
// the API key, so nothing needs redacting here; the Authorization header is
// logged separately in redacted form.
func printRequestJSON(request *ChatRequest) {
	raw, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		fmt.Fprintf(output, "(failed to marshal request: %v)\n", err)
		return
	}
	fmt.Fprintf(output, "\n--- Request JSON (Authorization header redacted, key not in body) ---\n%s\n", raw)
}

// inspectResponse walks the parsed struct and prints each field of interest.
func inspectResponse(response *ChatResponse) {
	fmt.Fprintln(output, "\n--- Struct inspection ---")
	fmt.Fprintf(output, "response.ID        = %q  (use for tracing)\n", response.ID)
	fmt.Fprintf(output, "response.Model     = %q\n", response.Model)
	fmt.Fprintf(output, "response.Provider  = %q\n", response.Provider)
	for _, choice := range response.Choices {
		fmt.Fprintf(output, "choice[%d].FinishReason       = %q\n", choice.Index, choice.FinishReason)
		fmt.Fprintf(output, "choice[%d].NativeFinishReason = %q  (upstream provider's stop reason)\n",
			choice.Index, choice.NativeFinishReason)
		if choice.Message.Content != "" {
			fmt.Fprintf(output, "choice[%d].Message.Content    = %q\n", choice.Index, choice.Message.Content)
		}
		for i, toolCall := range choice.Message.ToolCalls {
			fmt.Fprintf(output, "choice[%d].ToolCalls[%d]: id=%q name=%q arguments=%s\n",
				choice.Index, i, toolCall.ID, toolCall.Function.Name, toolCall.Function.Arguments)
		}
	}
	fmt.Fprintf(output, "usage.PromptTokens=%d CompletionTokens=%d TotalTokens=%d\n",
		response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens)
}

// explainOutcome prints a human explanation of what the model did.
func explainOutcome(choice Choice, toolRequestedButNotRun bool) {
	fmt.Fprintln(output, "\n--- What happened ---")
	switch {
	case toolRequestedButNotRun:
		fmt.Fprintln(output, "The model chose to CALL A TOOL instead of answering in text:")
		for _, toolCall := range choice.Message.ToolCalls {
			fmt.Fprintf(output, "  it asked for %s(%s) — finish_reason=%q means it is now waiting\n",
				toolCall.Function.Name, toolCall.Function.Arguments, choice.FinishReason)
		}
		fmt.Fprintln(output, "  for a tool result. This part stops here on purpose; the loop parts execute it.")
	case choice.FinishReason == finishReasonStop:
		fmt.Fprintf(output, "The model answered with TEXT (finish_reason=%q): %q\n",
			choice.FinishReason, choice.Message.Content)
	default:
		fmt.Fprintf(output, "The model stopped with finish_reason=%q\n", choice.FinishReason)
	}
}

// printComparison summarizes explicit-model vs routed runs side by side.
func printComparison(summaries []partSummary) {
	printSection("COMPARISON: explicit model vs. model routing")

	fmt.Fprintf(output, "%-52s %-14s %-38s %-10s %5s %7s %7s %6s\n",
		"Part", "Requested", "Served by (model)", "Provider", "Reqs", "PromTk", "CompTk", "Tools")
	for _, s := range summaries {
		fmt.Fprintf(output, "%-52s %-14s %-38s %-10s %5d %7d %7d %6d\n",
			truncate(s.Title, 50), s.RequestedModel,
			strings.Join(s.ModelsSeen, ", "), strings.Join(s.Providers, ", "),
			s.Requests, s.PromptTokens, s.CompletionTokens, s.ToolCallsMade)
	}

	fmt.Fprintln(output, "\nObservations (explicit Sonnet 5 vs openrouter/auto):")
	pairNote(summaries, 0, 3, "Part 1 vs 4 (simple call)")
	pairNote(summaries, 1, 4, "Part 2 vs 5 (tool loop)")
	pairNote(summaries, 2, 5, "Part 3 vs 6 (multiple tools)")

	fmt.Fprintln(output, "\nObservations (openrouter/auto vs openrouter/auto-beta):")
	pairNote(summaries, 3, 6, "Part 4 vs 7 (simple call)")
	pairNote(summaries, 4, 7, "Part 5 vs 8 (tool loop)")
	pairNote(summaries, 5, 8, "Part 6 vs 9 (multiple tools)")
}

// pairNote compares two parts (baseline vs variant) that ran the same prompt.
func pairNote(summaries []partSummary, baseline, variant int, label string) {
	if baseline >= len(summaries) || variant >= len(summaries) {
		return
	}
	b, v := summaries[baseline], summaries[variant]
	sameModel := strings.Join(b.ModelsSeen, ",") == strings.Join(v.ModelsSeen, ",")
	consistency := "routing stayed on ONE model across the conversation"
	if len(v.ModelsSeen) > 1 {
		consistency = fmt.Sprintf("routing SWITCHED models mid-conversation: %v", v.ModelsSeen)
	}
	fmt.Fprintf(output, "- %s: %s=%v %s=%v (same model: %t); %s; tokens %d vs %d (delta %+d)\n",
		label, b.RequestedModel, b.ModelsSeen, v.RequestedModel, v.ModelsSeen, sameModel, consistency,
		b.PromptTokens+b.CompletionTokens, v.PromptTokens+v.CompletionTokens,
		(v.PromptTokens+v.CompletionTokens)-(b.PromptTokens+b.CompletionTokens))
}

// requestedModelLabel names the model field for display.
func requestedModelLabel(model ModelName) string {
	switch model {
	case ModelRouted:
		return "(auto)"
	case ModelRoutedBeta:
		return "(auto-beta)"
	default:
		return string(model)
	}
}

// appendUnique appends value if not already present, preserving order.
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
