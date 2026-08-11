package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
)

// requestTimeout bounds each streaming request end to end.
const requestTimeout = 120 * time.Second

// Exchange is one prior user/assistant turn, used to carry conversation
// history into follow-up requests for the sticky-session test.
type Exchange struct {
	User      string
	Assistant string
}

// RequestSpec describes one test request.
type RequestSpec struct {
	Iteration      int
	Phase          string // score-sweep | sticky-first | sticky-follow-up | sticky-control
	PromptType     string // simple | complex | follow-up
	Prompt         string
	History        []Exchange
	MinCodingScore *float64
	SessionID      string // empty = omit session_id
}

// Result captures everything observed about one request. It is the unit
// written to the console, results.csv and results.json.
type Result struct {
	Timestamp      time.Time `json:"timestamp"`
	Iteration      int       `json:"iteration"`
	Phase          string    `json:"phase"`
	PromptType     string    `json:"prompt_type"`
	MinCodingScore *float64  `json:"min_coding_score"` // nil = omitted from request
	SessionID      string    `json:"session_id,omitempty"`
	ModelSelected  string    `json:"model_selected"`
	Cost           float64   `json:"cost_usd"`
	CostKnown      bool      `json:"cost_known"` // false if the API omitted usage.cost
	InputTokens    int64     `json:"input_tokens"`
	OutputTokens   int64     `json:"output_tokens"`
	TTFTMs         int64     `json:"ttft_ms"` // time to first content token
	TotalMs        int64     `json:"response_time_ms"`
	FinishReason   string    `json:"finish_reason,omitempty"`
	ReasoningChars int       `json:"reasoning_chars,omitempty"` // volume of hidden thinking, if streamed
	ResponseText   string    `json:"response_text,omitempty"`
	// Request is the wire request as the SDK serialized it, so results.json
	// records exactly what was sent, not a reconstruction from RequestSpec.
	Request json.RawMessage `json:"request,omitempty"`
	Err     string          `json:"error,omitempty"`
}

// Runner executes test requests against the Pareto router.
type Runner struct {
	client    *openrouter.OpenRouter
	maxTokens int64
}

// Run sends one streaming chat completion and collects routing/cost/latency
// data. Errors are returned inside the Result (Err field) rather than as a
// second value so a failed request never aborts the whole suite.
func (r *Runner) Run(ctx context.Context, spec RequestSpec) Result {
	res := Result{
		Timestamp:      time.Now(),
		Iteration:      spec.Iteration,
		Phase:          spec.Phase,
		PromptType:     spec.PromptType,
		MinCodingScore: spec.MinCodingScore,
		SessionID:      spec.SessionID,
	}

	if err := validatePrompt(spec.Prompt); err != nil {
		res.Err = fmt.Sprintf("invalid prompt: %v", err)
		return res
	}

	req := r.buildRequest(spec)
	if raw, err := json.Marshal(req); err == nil {
		res.Request = raw
	} else {
		fmt.Printf("  warning: could not serialize request for logging: %v\n", err)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	start := time.Now()
	resp, err := r.client.Chat.Send(ctx, req, nil)
	if err != nil {
		res.TotalMs = time.Since(start).Milliseconds()
		res.Err = fmt.Sprintf("request failed: %v", err)
		return res
	}
	if resp == nil || resp.EventStream == nil {
		res.TotalMs = time.Since(start).Milliseconds()
		res.Err = "unexpected response: no event stream returned for a streaming request"
		return res
	}

	es := resp.EventStream
	defer func() {
		if cerr := es.Close(); cerr != nil && res.Err == "" {
			res.Err = fmt.Sprintf("closing stream: %v", cerr)
		}
	}()

	var content strings.Builder
	reasoningChars := 0
	firstToken := time.Time{}

	for es.Next() {
		ev := es.Value()
		if ev == nil {
			continue
		}
		chunk := ev.Data

		// OWASP A08: mid-stream errors arrive as a field on the chunk;
		// surface them instead of silently trusting a truncated response.
		if chunk.Error != nil {
			b, merr := json.Marshal(chunk.Error)
			if merr != nil {
				res.Err = "provider reported a mid-stream error (unserializable)"
			} else {
				res.Err = "provider reported a mid-stream error: " + string(b)
			}
			break
		}

		if chunk.Model != "" {
			res.ModelSelected = chunk.Model
		}
		for _, choice := range chunk.Choices {
			// Reasoning models stream hidden thinking before visible text;
			// either kind counts as the first token for TTFT.
			if delta, ok := optVal(choice.Delta.Reasoning); ok && delta != "" {
				if firstToken.IsZero() {
					firstToken = time.Now()
				}
				reasoningChars += len(delta)
			}
			if delta, ok := optVal(choice.Delta.Content); ok && delta != "" {
				if firstToken.IsZero() {
					firstToken = time.Now()
				}
				content.WriteString(delta)
			}
			if choice.FinishReason != nil {
				res.FinishReason = string(*choice.FinishReason)
			}
		}
		if chunk.Usage != nil {
			res.InputTokens = chunk.Usage.PromptTokens
			res.OutputTokens = chunk.Usage.CompletionTokens
			if cost, ok := optVal(chunk.Usage.Cost); ok {
				res.Cost = cost
				res.CostKnown = true
			}
		}
	}
	if serr := es.Err(); serr != nil && res.Err == "" {
		res.Err = fmt.Sprintf("stream failed: %v", serr)
	}

	res.TotalMs = time.Since(start).Milliseconds()
	if !firstToken.IsZero() {
		res.TTFTMs = firstToken.Sub(start).Milliseconds()
	}
	res.ResponseText = content.String()
	res.ReasoningChars = reasoningChars

	// OWASP A08: a "successful" stream with no model and no output at all is
	// not a response we should trust silently. A reasoning-only response that
	// hit the token cap (finish_reason=length) is a valid observation, not an
	// error — the model ran out of budget while thinking.
	if res.Err == "" && res.ModelSelected == "" {
		res.Err = "response validation failed: no model reported in any chunk"
	}
	if res.Err == "" && res.ResponseText == "" && res.ReasoningChars == 0 {
		res.Err = "response validation failed: stream completed with no content or reasoning"
	}
	return res
}

func (r *Runner) buildRequest(spec RequestSpec) components.ChatRequest {
	msgs := make([]components.ChatMessages, 0, 2*len(spec.History)+1)
	for _, ex := range spec.History {
		msgs = append(msgs, components.CreateChatMessagesUser(components.ChatUserMessage{
			Role:    components.ChatUserMessageRoleUser,
			Content: components.CreateChatUserMessageContentStr(ex.User),
		}))
		assistantContent := components.CreateChatAssistantMessageContentStr(ex.Assistant)
		msgs = append(msgs, components.CreateChatMessagesAssistant(components.ChatAssistantMessage{
			Role:    components.ChatAssistantMessageRoleAssistant,
			Content: optionalnullable.From(&assistantContent),
		}))
	}
	msgs = append(msgs, components.CreateChatMessagesUser(components.ChatUserMessage{
		Role:    components.ChatUserMessageRoleUser,
		Content: components.CreateChatUserMessageContentStr(spec.Prompt),
	}))

	maxTok := r.maxTokens
	req := components.ChatRequest{
		Model:     ptr(paretoModel),
		Messages:  msgs,
		Stream:    ptr(true),
		MaxTokens: optionalnullable.From(&maxTok),
		Plugins: []components.ChatRequestPlugin{
			components.CreateChatRequestPluginParetoRouter(components.ParetoRouterPlugin{
				ID:             components.ParetoRouterPluginIDParetoRouter,
				MinCodingScore: spec.MinCodingScore,
			}),
		},
	}
	if spec.SessionID != "" {
		req.SessionID = ptr(spec.SessionID)
	}
	return req
}

func ptr[T any](v T) *T { return &v }

// optVal unwraps the SDK's three-state OptionalNullable: it returns the value
// and true only when the field was present and non-null.
func optVal[T any](o optionalnullable.OptionalNullable[T]) (T, bool) {
	var zero T
	if o == nil {
		return zero, false
	}
	p, ok := o[true]
	if !ok || p == nil {
		return zero, false
	}
	return *p, true
}
