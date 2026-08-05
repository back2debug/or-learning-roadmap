package main

import (
	"encoding/json"
	"fmt"
)

// ChatRequest mirrors the OpenRouter (OpenAI-compatible) chat completions
// request body. The API key is deliberately NOT part of this struct — it
// travels only in the Authorization header.
type ChatRequest struct {
	// Model is omitted from the JSON when empty, which activates
	// OpenRouter's model routing (Parts 4-6).
	Model      string    `json:"model,omitempty"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice string    `json:"tool_choice,omitempty"`
}

// Message is a single conversation turn. Content stays non-omitempty so tool
// results with empty output still serialize a content field.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Tool wraps a function definition in the OpenAI tool envelope.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef declares a callable function. Parameters holds a JSON Schema
// object; json.RawMessage keeps it byte-exact without interface{} maps.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is the model's request to invoke a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and its arguments as a JSON string,
// which must be validated before execution (never trust model output).
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse mirrors the OpenRouter chat completions response.
type ChatResponse struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Object   string   `json:"object"`
	Created  int64    `json:"created"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
}

// Choice is one completion candidate. FinishReason is the normalized reason
// ("stop", "tool_calls", ...); NativeFinishReason is the upstream provider's
// original value (e.g. Anthropic's "end_turn").
type Choice struct {
	Index              int     `json:"index"`
	Message            Message `json:"message"`
	FinishReason       string  `json:"finish_reason"`
	NativeFinishReason string  `json:"native_finish_reason"`
}

// Usage reports token consumption for a single request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// apiErrorEnvelope is the error body OpenRouter returns on non-2xx statuses.
type apiErrorEnvelope struct {
	Error struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"error"`
}

// APIStatusError is returned for non-2xx HTTP responses. It carries the
// request ID for tracing but never the request body or credentials.
type APIStatusError struct {
	StatusCode int
	RequestID  string
	Message    string
}

// Error implements the error interface.
func (e *APIStatusError) Error() string {
	return fmt.Sprintf("openrouter API error: status=%d request_id=%q message=%q",
		e.StatusCode, e.RequestID, e.Message)
}

// Retryable reports whether the error represents a transient failure.
func (e *APIStatusError) Retryable() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
}
