package dialect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Responses implements the OpenAI Responses dialect
// (POST /api/v1/responses, beta; forwarded upstream without conversion).
type Responses struct{}

func (Responses) Name() string { return "responses" }

type respContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// respInputItem is one item of the Responses `input` array: a message, a
// replayed function call, or a function call output.
type respInputItem struct {
	Type    string            `json:"type"`
	Role    string            `json:"role,omitempty"`
	Content []respContentPart `json:"content,omitempty"`
	// function_call / function_call_output
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type respTextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type respText struct {
	Format *respTextFormat `json:"format,omitempty"`
}

type respReasoning struct {
	Effort string `json:"effort"`
}

type respRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []respInputItem   `json:"input"`
	Tools           []respTool        `json:"tools,omitempty"`
	ToolChoice      any               `json:"tool_choice,omitempty"`
	Text            *respText         `json:"text,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	TopK            *int              `json:"top_k,omitempty"`
	Seed            *int              `json:"seed,omitempty"`
	Stop            []string          `json:"stop,omitempty"`
	Reasoning       *respReasoning    `json:"reasoning,omitempty"`
	Stream          bool              `json:"stream,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	OutputConfig    json.RawMessage   `json:"output_config,omitempty"`
	Debug           *debugOptions     `json:"debug,omitempty"`
	Provider        *providerPrefs    `json:"provider,omitempty"`
}

func (Responses) BuildRequest(spec PromptSpec) (Request, BuildNotes, error) {
	notes := newNotes()

	input := make([]respInputItem, 0, len(spec.Turns))
	for _, t := range spec.Turns {
		switch {
		case t.ToolCallID != "":
			input = append(input, respInputItem{
				Type:   "function_call_output",
				CallID: t.ToolCallID,
				Output: t.Text,
			})
		case len(t.ToolCalls) > 0:
			if t.Text != "" {
				input = append(input, messageItem(string(t.Role), t.Text))
			}
			for _, tc := range t.ToolCalls {
				input = append(input, respInputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				})
			}
		default:
			input = append(input, messageItem(string(t.Role), t.Text))
		}
	}

	req := respRequest{
		Model:        spec.Model,
		Instructions: spec.System,
		Input:        input,
		Temperature:  spec.Temperature,
		TopP:         spec.TopP,
		TopK:         spec.TopK,
		Seed:         spec.Seed,
		Stop:         spec.StopSequences,
		Stream:       spec.Stream,
		SessionID:    spec.SessionID,
		Metadata:     spec.Metadata,
		OutputConfig: spec.OutputConfig,
		Debug:        debugFrom(spec),
		Provider:     providerFrom(spec),
	}
	if spec.System != "" {
		notes.Renamed["System"] = "instructions"
	}
	if spec.MaxTokens > 0 {
		req.MaxOutputTokens = spec.MaxTokens
		notes.Renamed["MaxTokens"] = "max_output_tokens"
	}
	if spec.TopK != nil {
		notes.Transformed["TopK"] = "top_k is not part of OpenAI's Responses schema; sent as an OpenRouter extension (probe)"
	}
	if spec.Seed != nil {
		notes.Transformed["Seed"] = "seed is not part of OpenAI's Responses schema; sent anyway (probe)"
	}
	if len(spec.StopSequences) > 0 {
		notes.Transformed["StopSequences"] = "stop is not part of OpenAI's Responses schema; sent anyway (probe)"
	}
	for _, td := range spec.Tools {
		req.Tools = append(req.Tools, respTool{
			Type:        "function",
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		})
	}
	if len(spec.Tools) > 0 {
		notes.Transformed["Tools"] = "flattened: name/parameters sit directly on the tool object, no function wrapper"
	}
	switch spec.ToolChoice.Mode {
	case "":
	case "named":
		req.ToolChoice = map[string]string{"type": "function", "name": spec.ToolChoice.Name}
		notes.Transformed["ToolChoice"] = `{"type":"function","name":…} (flattened)`
	default:
		req.ToolChoice = spec.ToolChoice.Mode
	}
	if spec.JSONSchema != nil {
		req.Text = &respText{Format: &respTextFormat{
			Type:   "json_schema",
			Name:   spec.JSONSchema.Name,
			Strict: spec.JSONSchema.Strict,
			Schema: spec.JSONSchema.Schema,
		}}
		notes.Renamed["JSONSchema"] = "text.format"
	}
	if spec.ReasoningEffort != "" {
		req.Reasoning = &respReasoning{Effort: spec.ReasoningEffort}
		notes.Renamed["ReasoningEffort"] = "reasoning.effort"
	}
	if len(spec.OutputConfig) > 0 {
		notes.Transformed["OutputConfig"] = "sent verbatim although absent from the Responses schema (probe)"
	}

	body, err := marshalBody(req)
	if err != nil {
		return Request{}, notes, err
	}
	return Request{Path: "/responses", Body: body}, notes, nil
}

func messageItem(role, text string) respInputItem {
	part := "input_text"
	if role == string(RoleAssistant) {
		part = "output_text"
	}
	return respInputItem{
		Type:    "message",
		Role:    role,
		Content: []respContentPart{{Type: part, Text: text}},
	}
}

type respUsage struct {
	InputTokens        *int     `json:"input_tokens"`
	OutputTokens       *int     `json:"output_tokens"`
	Cost               *float64 `json:"cost"`
	InputTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *respUsage) toUsage() Usage {
	out := Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		Cost:             u.Cost,
	}
	if u.InputTokensDetails != nil {
		out.CachedTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out
}

// respOutputItem is one item of a completed response's `output` array.
type respOutputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// reasoning
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

type respResponse struct {
	ID                string           `json:"id"`
	Status            string           `json:"status"`
	Provider          string           `json:"provider"`
	Output            []respOutputItem `json:"output"`
	Usage             *respUsage       `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Metadata json.RawMessage `json:"openrouter_metadata"`
}

func (rr *respResponse) apply(res *Result) {
	if rr.ID != "" {
		res.GenerationID = rr.ID
	}
	if rr.Provider != "" {
		res.Provider = rr.Provider
	}
	if rr.Usage != nil {
		res.Usage = rr.Usage.toUsage()
	}
	if len(rr.Metadata) > 0 && string(rr.Metadata) != "null" {
		res.RouterMetadata = rawCopy(rr.Metadata)
	}
	for _, item := range rr.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					res.Text += c.Text
				}
			}
		case "function_call":
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: json.RawMessage(item.Arguments),
			})
		case "reasoning":
			for _, s := range item.Summary {
				res.ReasoningText += s.Text
			}
		}
	}
	res.NativeStopReason = rr.Status
	switch rr.Status {
	case "completed":
		res.StopReason = "stop"
		if len(res.ToolCalls) > 0 {
			res.StopReason = "tool_use"
		}
	case "incomplete":
		res.StopReason = "other"
		if rr.IncompleteDetails != nil {
			res.NativeStopReason = "incomplete:" + rr.IncompleteDetails.Reason
			if rr.IncompleteDetails.Reason == "max_output_tokens" {
				res.StopReason = "length"
			}
		}
	case "failed":
		res.StopReason = "error"
	default:
		res.StopReason = "other"
	}
}

// respStreamEvent is the union of Responses SSE payloads we care about. The
// full event grammar is captured raw in StreamEvents for the grammar report.
type respStreamEvent struct {
	Type     string          `json:"type"`
	Delta    json.RawMessage `json:"delta"` // string for text deltas, object for others
	Response *respResponse   `json:"response"`
	Item     *respOutputItem `json:"item"`
	Debug    json.RawMessage `json:"debug"`
	Error    json.RawMessage `json:"error"`
	Metadata json.RawMessage `json:"openrouter_metadata"`
}

func (Responses) ParseStream(r StreamReader) (*Result, []StreamEvent, error) {
	res := &Result{Unsupported: map[string]string{}}
	var events []StreamEvent
	first := true
	sawTerminal := false

	for {
		rec, err := readSSE(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, events, fmt.Errorf("responses: reading stream: %w", err)
		}
		if string(rec.data) == "[DONE]" {
			break
		}
		events = append(events, StreamEvent{Type: rec.event, Data: safeRaw(rec.data), At: rec.at})
		if first {
			res.TTFB = rec.at
			first = false
		}

		var ev respStreamEvent
		if err := json.Unmarshal(rec.data, &ev); err != nil {
			return nil, events, fmt.Errorf("responses: malformed event %q: %w", truncate(rec.data), err)
		}
		typ := ev.Type
		if typ == "" {
			typ = rec.event
		}
		if events[len(events)-1].Type == "" {
			events[len(events)-1].Type = typ
		}

		switch typ {
		case "response.debug":
			if len(ev.Debug) > 0 && string(ev.Debug) != "null" {
				res.UpstreamBodies = append(res.UpstreamBodies, extractUpstreamBody(ev.Debug))
			} else {
				// Some shapes put the payload on the event itself.
				res.UpstreamBodies = append(res.UpstreamBodies, extractUpstreamBody(rec.data))
			}
		case "response.output_text.delta", "response.content_part.delta":
			var s string
			if err := json.Unmarshal(ev.Delta, &s); err == nil {
				res.Text += s
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			var s string
			if err := json.Unmarshal(ev.Delta, &s); err == nil {
				res.ReasoningText += s
			}
		case "response.completed", "response.done", "response.incomplete", "response.failed":
			sawTerminal = true
			if ev.Response != nil {
				if len(ev.Response.Output) > 0 {
					// The terminal snapshot is authoritative; keeping the
					// delta accumulation too would double-count.
					res.Text, res.ReasoningText, res.ToolCalls = "", "", nil
				}
				ev.Response.apply(res)
			}
			if len(ev.Metadata) > 0 && string(ev.Metadata) != "null" {
				res.RouterMetadata = rawCopy(ev.Metadata)
			}
		case "error", "response.error":
			res.StopReason = "error"
			res.NativeStopReason = "error"
		}
	}

	if !sawTerminal && res.StopReason == "" {
		res.StopReason = "other"
		res.NativeStopReason = "stream ended without a terminal response event"
	}
	return res, events, nil
}

func (Responses) ParseResponse(body []byte) (*Result, error) {
	var rr respResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, fmt.Errorf("responses: malformed response: %w", err)
	}
	res := &Result{Unsupported: map[string]string{
		"UpstreamBodies": "debug.echo_upstream_body is streaming-only",
		"TTFB":           "not measurable on a buffered response",
	}}
	rr.apply(res)
	return res, nil
}

var _ Dialect = Responses{}
