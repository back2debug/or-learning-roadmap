package dialect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Chat implements the OpenAI Chat Completions dialect
// (POST /api/v1/chat/completions).
type Chat struct{}

func (Chat) Name() string { return "chat" }

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string      `json:"type"`
	Function chatToolDef `json:"function"`
}

type chatToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *chatJSONSchema `json:"json_schema,omitempty"`
}

type chatJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatReasoning struct {
	Effort string `json:"effort"`
}

type chatRequest struct {
	Model               string              `json:"model"`
	Messages            []chatMessage       `json:"messages"`
	Tools               []chatTool          `json:"tools,omitempty"`
	ToolChoice          any                 `json:"tool_choice,omitempty"`
	ResponseFormat      *chatResponseFormat `json:"response_format,omitempty"`
	MaxCompletionTokens int                 `json:"max_completion_tokens,omitempty"`
	Temperature         *float64            `json:"temperature,omitempty"`
	TopP                *float64            `json:"top_p,omitempty"`
	TopK                *int                `json:"top_k,omitempty"`
	Stop                []string            `json:"stop,omitempty"`
	Seed                *int                `json:"seed,omitempty"`
	Reasoning           *chatReasoning      `json:"reasoning,omitempty"`
	Stream              bool                `json:"stream,omitempty"`
	SessionID           string              `json:"session_id,omitempty"`
	Metadata            map[string]string   `json:"metadata,omitempty"`
	// OutputConfig is not in the Chat schema; the extras_parity probe sends
	// it anyway to see whether chat rejects or ignores it.
	OutputConfig json.RawMessage `json:"output_config,omitempty"`
	Debug        *debugOptions   `json:"debug,omitempty"`
	Provider     *providerPrefs  `json:"provider,omitempty"`
}

func (Chat) BuildRequest(spec PromptSpec) (Request, BuildNotes, error) {
	notes := newNotes()

	msgs := make([]chatMessage, 0, len(spec.Turns)+1)
	if spec.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: spec.System})
		notes.Renamed["System"] = `messages[0] with role "system"`
	}
	for _, t := range spec.Turns {
		m := chatMessage{Role: string(t.Role), Content: t.Text}
		if t.ToolCallID != "" {
			m.Role = "tool"
			m.ToolCallID = t.ToolCallID
		}
		for _, tc := range t.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatToolFunction{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		msgs = append(msgs, m)
	}

	req := chatRequest{
		Model:        spec.Model,
		Messages:     msgs,
		Temperature:  spec.Temperature,
		TopP:         spec.TopP,
		TopK:         spec.TopK,
		Stop:         spec.StopSequences,
		Seed:         spec.Seed,
		Stream:       spec.Stream,
		SessionID:    spec.SessionID,
		Metadata:     spec.Metadata,
		OutputConfig: spec.OutputConfig,
		Debug:        debugFrom(spec),
		Provider:     providerFrom(spec),
	}
	if spec.MaxTokens > 0 {
		req.MaxCompletionTokens = spec.MaxTokens
		notes.Renamed["MaxTokens"] = "max_completion_tokens"
	}
	if len(spec.StopSequences) > 0 {
		notes.Renamed["StopSequences"] = "stop"
	}
	for _, td := range spec.Tools {
		req.Tools = append(req.Tools, chatTool{Type: "function", Function: chatToolDef(td)})
	}
	if len(spec.Tools) > 0 {
		notes.Transformed["Tools"] = `wrapped as tools[].function`
	}
	switch spec.ToolChoice.Mode {
	case "":
	case "named":
		req.ToolChoice = map[string]any{
			"type":     "function",
			"function": map[string]string{"name": spec.ToolChoice.Name},
		}
		notes.Transformed["ToolChoice"] = `{"type":"function","function":{"name":…}}`
	default:
		req.ToolChoice = spec.ToolChoice.Mode
	}
	if spec.JSONSchema != nil {
		req.ResponseFormat = &chatResponseFormat{
			Type: "json_schema",
			JSONSchema: &chatJSONSchema{
				Name:   spec.JSONSchema.Name,
				Strict: spec.JSONSchema.Strict,
				Schema: spec.JSONSchema.Schema,
			},
		}
		notes.Renamed["JSONSchema"] = "response_format.json_schema"
	}
	if spec.ReasoningEffort != "" {
		req.Reasoning = &chatReasoning{Effort: spec.ReasoningEffort}
		notes.Renamed["ReasoningEffort"] = "reasoning.effort"
	}
	if len(spec.OutputConfig) > 0 {
		notes.Transformed["OutputConfig"] = "sent verbatim although absent from the Chat schema (probe)"
	}

	body, err := marshalBody(req)
	if err != nil {
		return Request{}, notes, err
	}
	return Request{Path: "/chat/completions", Body: body}, notes, nil
}

// chatUsage mirrors the always-included usage block.
type chatUsage struct {
	PromptTokens        *int     `json:"prompt_tokens"`
	CompletionTokens    *int     `json:"completion_tokens"`
	Cost                *float64 `json:"cost"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *chatUsage) toUsage() Usage {
	out := Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		Cost:             u.Cost,
	}
	if u.PromptTokensDetails != nil {
		out.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

type chatChunk struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Choices  []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason       *string `json:"finish_reason"`
		NativeFinishReason string  `json:"native_finish_reason"`
	} `json:"choices"`
	Usage    *chatUsage      `json:"usage"`
	Error    json.RawMessage `json:"error"`
	Debug    json.RawMessage `json:"debug"`
	Metadata json.RawMessage `json:"openrouter_metadata"`
}

func (Chat) ParseStream(r StreamReader) (*Result, []StreamEvent, error) {
	res := &Result{Unsupported: map[string]string{}}
	var events []StreamEvent
	toolArgs := map[int]*ToolCall{} // accumulate streamed tool-call fragments by index
	var toolOrder []int
	first := true

	for {
		rec, err := readSSE(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, events, fmt.Errorf("chat: reading stream: %w", err)
		}
		if string(rec.data) == "[DONE]" {
			break
		}
		events = append(events, StreamEvent{Type: rec.event, Data: safeRaw(rec.data), At: rec.at})
		if first {
			res.TTFB = rec.at
			first = false
		}

		var ch chatChunk
		if err := json.Unmarshal(rec.data, &ch); err != nil {
			return nil, events, fmt.Errorf("chat: malformed chunk %q: %w", truncate(rec.data), err)
		}
		if ch.ID != "" {
			res.GenerationID = ch.ID
		}
		if ch.Provider != "" {
			res.Provider = ch.Provider
		}
		if len(ch.Debug) > 0 && string(ch.Debug) != "null" {
			res.UpstreamBodies = append(res.UpstreamBodies, extractUpstreamBody(ch.Debug))
		}
		if len(ch.Metadata) > 0 && string(ch.Metadata) != "null" {
			res.RouterMetadata = rawCopy(ch.Metadata)
		}
		if len(ch.Error) > 0 && string(ch.Error) != "null" {
			res.StopReason = "error"
			res.NativeStopReason = "error"
			continue
		}
		if ch.Usage != nil {
			res.Usage = ch.Usage.toUsage()
		}
		for _, choice := range ch.Choices {
			res.Text += choice.Delta.Content
			res.ReasoningText += choice.Delta.Reasoning
			for i, tc := range choice.Delta.ToolCalls {
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				acc, ok := toolArgs[idx]
				if !ok {
					acc = &ToolCall{}
					toolArgs[idx] = acc
					toolOrder = append(toolOrder, idx)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				acc.Arguments = append(acc.Arguments, tc.Function.Arguments...)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				res.StopReason = normalizeChatStop(*choice.FinishReason)
				res.NativeStopReason = *choice.FinishReason
			}
			if choice.NativeFinishReason != "" {
				res.NativeStopReason = choice.NativeFinishReason
			}
		}
	}

	for _, idx := range toolOrder {
		res.ToolCalls = append(res.ToolCalls, *toolArgs[idx])
	}
	return res, events, nil
}

type chatResponse struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Choices  []struct {
		Message struct {
			Content   string         `json:"content"`
			Reasoning string         `json:"reasoning"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason       string `json:"finish_reason"`
		NativeFinishReason string `json:"native_finish_reason"`
	} `json:"choices"`
	Usage    *chatUsage      `json:"usage"`
	Metadata json.RawMessage `json:"openrouter_metadata"`
}

func (Chat) ParseResponse(body []byte) (*Result, error) {
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, fmt.Errorf("chat: malformed response: %w", err)
	}
	res := &Result{
		GenerationID: cr.ID,
		Provider:     cr.Provider,
		Unsupported: map[string]string{
			"UpstreamBodies": "debug.echo_upstream_body is streaming-only",
			"TTFB":           "not measurable on a buffered response",
		},
	}
	if cr.Usage != nil {
		res.Usage = cr.Usage.toUsage()
	}
	if len(cr.Metadata) > 0 && string(cr.Metadata) != "null" {
		res.RouterMetadata = rawCopy(cr.Metadata)
	}
	for _, choice := range cr.Choices {
		res.Text += choice.Message.Content
		res.ReasoningText += choice.Message.Reasoning
		for _, tc := range choice.Message.ToolCalls {
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			})
		}
		if choice.FinishReason != "" {
			res.StopReason = normalizeChatStop(choice.FinishReason)
			res.NativeStopReason = choice.FinishReason
		}
		if choice.NativeFinishReason != "" {
			res.NativeStopReason = choice.NativeFinishReason
		}
	}
	return res, nil
}

func normalizeChatStop(reason string) string {
	switch reason {
	case "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "content_filter"
	case "error":
		return "error"
	default:
		return "other"
	}
}

func truncate(b []byte) string {
	const n = 120
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "… (" + strconv.Itoa(len(b)) + " bytes)"
}

var _ Dialect = Chat{}
