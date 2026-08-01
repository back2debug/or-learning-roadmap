package main

import "encoding/json"

// ---------- Request ----------

type ChatRequest struct {
	Model     string           `json:"model"`
	Messages  []Message        `json:"messages"`
	Stream    bool             `json:"stream,omitempty"`
	Reasoning *ReasoningConfig `json:"reasoning,omitempty"`
	Tools     []Tool           `json:"tools,omitempty"`
	// Ask OpenRouter to include cost/usage accounting in the response.
	Usage *UsageRequest `json:"usage,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string, not an object
}

// ReasoningConfig is OpenRouter's unified reasoning parameter. Set at most
// one of Effort or MaxTokens; Exclude runs reasoning but strips it from the
// response. Pointers so that "not set" is distinguishable from zero values.
type ReasoningConfig struct {
	Effort    string `json:"effort,omitempty"`     // "xhigh"|"high"|"medium"|"low"|"minimal"|"none"
	MaxTokens int    `json:"max_tokens,omitempty"` // budget-style (Anthropic/Gemini)
	Exclude   *bool  `json:"exclude,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

type UsageRequest struct {
	Include bool `json:"include"`
}

// ---------- Messages (used in both directions) ----------

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Tool calling: assistant messages carry tool_calls; role:"tool" replies
	// reference the call they answer via tool_call_id.
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Populated only on responses from reasoning models:
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"`
}

// ReasoningDetail is one structured reasoning block. Which payload field is
// set depends on Type: reasoning.text -> Text (+Signature),
// reasoning.summary -> Summary, reasoning.encrypted -> Data.
type ReasoningDetail struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Format    string `json:"format,omitempty"` // e.g. "anthropic-claude-v1", "openai-responses-v1"
	Index     int    `json:"index,omitempty"`
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Data      string `json:"data,omitempty"`
}

// ---------- Response ----------

type ChatResponse struct {
	ID       string   `json:"id"`       // OpenRouter generation id ("gen-...")
	Provider string   `json:"provider"` // OpenRouter addition: which upstream served it
	Model    string   `json:"model"`
	Object   string   `json:"object"`
	Created  int64    `json:"created"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
}

type Choice struct {
	Index              int     `json:"index"`
	Message            Message `json:"message"`
	FinishReason       string  `json:"finish_reason"`
	NativeFinishReason string  `json:"native_finish_reason"` // OpenRouter addition: provider's raw value
}

type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	Cost                    float64                  `json:"cost,omitempty"` // USD, when usage.include=true
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}
