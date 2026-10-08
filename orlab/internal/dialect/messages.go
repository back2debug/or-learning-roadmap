package dialect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Messages implements the Anthropic Messages dialect
// (POST /api/v1/messages).
type Messages struct{}

func (Messages) Name() string { return "messages" }

// effortToBudget maps chat's effort vocabulary onto thinking budget_tokens.
// The mapping is this harness's own convention (recorded in BuildNotes);
// Anthropic's minimum budget is 1024.
var effortToBudget = map[string]int{
	"minimal": 1024,
	"low":     2048,
	"medium":  4096,
	"high":    8192,
	"xhigh":   16384,
	"max":     32768,
}

type msgContentBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// thinking (responses only; never sent)
	Thinking string `json:"thinking,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type msgMessage struct {
	Role    string            `json:"role"`
	Content []msgContentBlock `json:"content"`
}

type msgTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type msgToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type msgThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type msgOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type msgOutputConfig struct {
	Format *msgOutputFormat `json:"format,omitempty"`
}

type msgRequest struct {
	Model         string            `json:"model"`
	System        string            `json:"system,omitempty"`
	Messages      []msgMessage      `json:"messages"`
	Tools         []msgTool         `json:"tools,omitempty"`
	ToolChoice    *msgToolChoice    `json:"tool_choice,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	TopK          *int              `json:"top_k,omitempty"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
	Thinking      *msgThinking      `json:"thinking,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	SessionID     string            `json:"session_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	OutputConfig  json.RawMessage   `json:"output_config,omitempty"`
	// Debug is documented as unsupported here; sending it anyway is the probe.
	Debug    *debugOptions  `json:"debug,omitempty"`
	Provider *providerPrefs `json:"provider,omitempty"`
}

func (Messages) BuildRequest(spec PromptSpec) (Request, BuildNotes, error) {
	notes := newNotes()

	msgs := make([]msgMessage, 0, len(spec.Turns))
	for _, t := range spec.Turns {
		switch {
		case t.ToolCallID != "":
			msgs = append(msgs, msgMessage{Role: "user", Content: []msgContentBlock{{
				Type:      "tool_result",
				ToolUseID: t.ToolCallID,
				Content:   t.Text,
			}}})
		case len(t.ToolCalls) > 0:
			blocks := make([]msgContentBlock, 0, len(t.ToolCalls)+1)
			if t.Text != "" {
				blocks = append(blocks, msgContentBlock{Type: "text", Text: t.Text})
			}
			for _, tc := range t.ToolCalls {
				blocks = append(blocks, msgContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Arguments,
				})
			}
			msgs = append(msgs, msgMessage{Role: string(t.Role), Content: blocks})
		default:
			msgs = append(msgs, msgMessage{Role: string(t.Role), Content: []msgContentBlock{{
				Type: "text", Text: t.Text,
			}}})
		}
	}

	req := msgRequest{
		Model:         spec.Model,
		System:        spec.System,
		Messages:      msgs,
		MaxTokens:     spec.MaxTokens,
		Temperature:   spec.Temperature,
		TopP:          spec.TopP,
		TopK:          spec.TopK,
		StopSequences: spec.StopSequences,
		Stream:        spec.Stream,
		SessionID:     spec.SessionID,
		Metadata:      spec.Metadata,
		OutputConfig:  spec.OutputConfig,
		Debug:         debugFrom(spec),
		Provider:      providerFrom(spec),
	}
	if spec.System != "" {
		notes.Renamed["System"] = "top-level system"
	}
	if len(spec.StopSequences) > 0 {
		notes.Renamed["StopSequences"] = "stop_sequences"
	}
	if spec.Seed != nil {
		notes.Unexpressible["Seed"] = "the Messages schema has no seed field"
	}
	for _, td := range spec.Tools {
		req.Tools = append(req.Tools, msgTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.Parameters,
		})
	}
	if len(spec.Tools) > 0 {
		notes.Renamed["Tools[].Parameters"] = "tools[].input_schema"
	}
	switch spec.ToolChoice.Mode {
	case "":
	case "auto":
		req.ToolChoice = &msgToolChoice{Type: "auto"}
	case "none":
		req.ToolChoice = &msgToolChoice{Type: "none"}
	case "required":
		req.ToolChoice = &msgToolChoice{Type: "any"}
		notes.Transformed["ToolChoice"] = `"required" becomes {"type":"any"}`
	case "named":
		req.ToolChoice = &msgToolChoice{Type: "tool", Name: spec.ToolChoice.Name}
		notes.Transformed["ToolChoice"] = `named choice becomes {"type":"tool","name":…}`
	}
	if spec.JSONSchema != nil && len(spec.OutputConfig) == 0 {
		oc, err := json.Marshal(msgOutputConfig{Format: &msgOutputFormat{
			Type:   "json_schema",
			Schema: spec.JSONSchema.Schema,
		}})
		if err != nil {
			return Request{}, notes, fmt.Errorf("messages: encoding output_config: %w", err)
		}
		req.OutputConfig = oc
		notes.Renamed["JSONSchema"] = "output_config.format"
		notes.Transformed["JSONSchema"] = "name/strict have no wire form; schema only"
	}
	if spec.ReasoningEffort != "" {
		if spec.ReasoningEffort == "none" {
			req.Thinking = &msgThinking{Type: "disabled"}
			notes.Transformed["ReasoningEffort"] = `"none" becomes thinking {"type":"disabled"}`
		} else if budget, ok := effortToBudget[spec.ReasoningEffort]; ok {
			req.Thinking = &msgThinking{Type: "enabled", BudgetTokens: budget}
			notes.Transformed["ReasoningEffort"] = fmt.Sprintf(
				"%q becomes thinking budget_tokens=%d (harness convention)", spec.ReasoningEffort, budget)
		} else {
			notes.Unexpressible["ReasoningEffort"] = fmt.Sprintf("unknown effort %q", spec.ReasoningEffort)
		}
	}
	if spec.EchoUpstream {
		notes.Transformed["EchoUpstream"] = "debug sent although docs say unsupported on /messages (probe)"
	}
	if spec.MaxTokens > 0 {
		notes.Renamed["MaxTokens"] = "max_tokens"
	}

	body, err := marshalBody(req)
	if err != nil {
		return Request{}, notes, err
	}
	return Request{Path: "/messages", Body: body}, notes, nil
}

type msgUsage struct {
	InputTokens          *int     `json:"input_tokens"`
	OutputTokens         *int     `json:"output_tokens"`
	CacheReadInputTokens *int     `json:"cache_read_input_tokens"`
	Cost                 *float64 `json:"cost"`
}

func (u *msgUsage) apply(res *Result) {
	if u.InputTokens != nil {
		res.Usage.PromptTokens = u.InputTokens
	}
	if u.OutputTokens != nil {
		res.Usage.CompletionTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens != nil {
		res.Usage.CachedTokens = u.CacheReadInputTokens
	}
	if u.Cost != nil {
		res.Usage.Cost = u.Cost
	}
}

// msgStreamEvent is the union of every Messages SSE payload we care about.
type msgStreamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string    `json:"id"`
		Usage *msgUsage `json:"usage"`
	} `json:"message"`
	Index        *int             `json:"index"`
	ContentBlock *msgContentBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
		StopSeq     string `json:"stop_sequence"`
	} `json:"delta"`
	Usage    *msgUsage       `json:"usage"`
	Error    json.RawMessage `json:"error"`
	Debug    json.RawMessage `json:"debug"`
	Metadata json.RawMessage `json:"openrouter_metadata"`
}

func (Messages) ParseStream(r StreamReader) (*Result, []StreamEvent, error) {
	res := &Result{Unsupported: map[string]string{
		"Provider": "the Messages envelope does not name the serving provider inline",
	}}
	var events []StreamEvent
	blocks := map[int]*ToolCall{} // tool_use blocks by content index
	var blockOrder []int
	first := true

	for {
		rec, err := readSSE(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, events, fmt.Errorf("messages: reading stream: %w", err)
		}
		// OpenRouter appends an OpenAI-style terminator to the Anthropic
		// grammar ("event: data" / "data: [DONE]"). Anthropic's own API ends
		// at message_stop, so a native Messages client would choke here.
		if string(rec.data) == "[DONE]" {
			break
		}
		events = append(events, StreamEvent{Type: rec.event, Data: safeRaw(rec.data), At: rec.at})
		if first {
			res.TTFB = rec.at
			first = false
		}

		var ev msgStreamEvent
		if err := json.Unmarshal(rec.data, &ev); err != nil {
			return nil, events, fmt.Errorf("messages: malformed event %q: %w", truncate(rec.data), err)
		}
		typ := ev.Type
		if typ == "" {
			typ = rec.event
		}
		switch typ {
		case "message_start":
			if ev.Message != nil {
				res.GenerationID = ev.Message.ID
				if ev.Message.Usage != nil {
					ev.Message.Usage.apply(res)
				}
			}
		case "content_block_start":
			if ev.Index != nil && ev.ContentBlock != nil {
				if ev.ContentBlock.Type == "tool_use" {
					blocks[*ev.Index] = &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
					blockOrder = append(blockOrder, *ev.Index)
				}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				break
			}
			switch ev.Delta.Type {
			case "text_delta":
				res.Text += ev.Delta.Text
			case "thinking_delta":
				res.ReasoningText += ev.Delta.Thinking
			case "input_json_delta":
				if ev.Index != nil {
					if acc, ok := blocks[*ev.Index]; ok {
						acc.Arguments = append(acc.Arguments, ev.Delta.PartialJSON...)
					}
				}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				res.StopReason = normalizeMessagesStop(ev.Delta.StopReason)
				res.NativeStopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				ev.Usage.apply(res)
			}
		case "message_stop":
			if len(ev.Metadata) > 0 && string(ev.Metadata) != "null" {
				res.RouterMetadata = rawCopy(ev.Metadata)
			}
		case "error":
			res.StopReason = "error"
			res.NativeStopReason = "error"
		case "ping":
		}
		// Echo support on /messages is undocumented-to-absent; capture it
		// wherever it might surface so the probe records evidence.
		if len(ev.Debug) > 0 && string(ev.Debug) != "null" {
			res.UpstreamBodies = append(res.UpstreamBodies, extractUpstreamBody(ev.Debug))
		}
	}

	for _, idx := range blockOrder {
		res.ToolCalls = append(res.ToolCalls, *blocks[idx])
	}
	return res, events, nil
}

type msgResponse struct {
	ID         string            `json:"id"`
	Content    []msgContentBlock `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      *msgUsage         `json:"usage"`
	Metadata   json.RawMessage   `json:"openrouter_metadata"`
	// thinking blocks arrive inside content with type "thinking".
	Model string `json:"model"`
}

func (Messages) ParseResponse(body []byte) (*Result, error) {
	var mr msgResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("messages: malformed response: %w", err)
	}
	res := &Result{
		GenerationID: mr.ID,
		Unsupported: map[string]string{
			"Provider":       "the Messages envelope does not name the serving provider inline",
			"UpstreamBodies": "debug.echo_upstream_body is streaming-only (and undocumented on /messages)",
			"TTFB":           "not measurable on a buffered response",
		},
	}
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			res.Text += b.Text
		case "thinking":
			res.ReasoningText += b.Thinking
		case "tool_use":
			res.ToolCalls = append(res.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
		}
	}
	if mr.StopReason != "" {
		res.StopReason = normalizeMessagesStop(mr.StopReason)
		res.NativeStopReason = mr.StopReason
	}
	if mr.Usage != nil {
		mr.Usage.apply(res)
	}
	if len(mr.Metadata) > 0 && string(mr.Metadata) != "null" {
		res.RouterMetadata = rawCopy(mr.Metadata)
	}
	return res, nil
}

func normalizeMessagesStop(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_use"
	case "refusal":
		return "content_filter"
	default:
		return "other"
	}
}

var _ Dialect = Messages{}
