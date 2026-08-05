package main

// Run the tests with:
//
//	go test ./...
//
// Or verbosely:
//
//	go test -v ./...
//
// The tests never touch the network: Client.httpClient is the HTTPDoer
// interface, so we inject a mock that returns canned *http.Response values.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

// mockDoer implements HTTPDoer and replays a scripted sequence of responses.
type mockDoer struct {
	responses []*http.Response
	errs      []error
	calls     int
}

func (m *mockDoer) Do(req *http.Request) (*http.Response, error) {
	index := m.calls
	m.calls++
	if index < len(m.errs) && m.errs[index] != nil {
		return nil, m.errs[index]
	}
	return m.responses[index], nil
}

// jsonResponse builds a canned HTTP response with a JSON body.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}

// newTestClient builds a Client around a mock without touching the env var.
func newTestClient(doer HTTPDoer) *Client {
	return &Client{
		httpClient: doer,
		apiKey:     "sk-or-test-key-not-real",
		baseURL:    "https://example.invalid/api/v1",
		logger:     slog.New(slog.DiscardHandler),
	}
}

const successBody = `{
	"id": "gen-123",
	"provider": "Anthropic",
	"model": "anthropic/claude-sonnet-5",
	"choices": [{
		"index": 0,
		"finish_reason": "tool_calls",
		"message": {
			"role": "assistant",
			"content": "",
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {"name": "add_numbers", "arguments": "{\"num1\":47,\"num2\":23}"}
			}]
		}
	}],
	"usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
}`

func TestCreateChatCompletionParsesToolCalls(t *testing.T) {
	client := newTestClient(&mockDoer{responses: []*http.Response{jsonResponse(200, successBody)}})

	response, _, err := client.CreateChatCompletion(context.Background(), &ChatRequest{
		Model:    string(ModelClaudeSonnet5),
		Messages: []Message{{Role: roleUser, Content: "What is 47 + 23?"}},
		Tools:    []Tool{addNumbersTool()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response.Model != "anthropic/claude-sonnet-5" {
		t.Errorf("Model = %q, want anthropic/claude-sonnet-5", response.Model)
	}
	choice := response.Choices[0]
	if choice.FinishReason != finishReasonToolCalls {
		t.Errorf("FinishReason = %q, want %q", choice.FinishReason, finishReasonToolCalls)
	}
	if got := choice.Message.ToolCalls[0].Function.Name; got != "add_numbers" {
		t.Errorf("tool name = %q, want add_numbers", got)
	}
}

func TestCreateChatCompletionRetriesOn500(t *testing.T) {
	doer := &mockDoer{responses: []*http.Response{
		jsonResponse(500, `{"error":{"message":"upstream exploded"}}`),
		jsonResponse(200, successBody),
	}}
	client := newTestClient(doer)

	_, _, err := client.CreateChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: roleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if doer.calls != 2 {
		t.Errorf("calls = %d, want 2 (one failure + one retry)", doer.calls)
	}
}

func TestCreateChatCompletionDoesNotRetry4xx(t *testing.T) {
	doer := &mockDoer{responses: []*http.Response{
		jsonResponse(400, `{"error":{"message":"bad request"}}`),
	}}
	client := newTestClient(doer)

	_, _, err := client.CreateChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: roleUser, Content: "hi"}},
	})
	var apiErr *APIStatusError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIStatusError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if doer.calls != 1 {
		t.Errorf("calls = %d, want 1 (4xx must not be retried)", doer.calls)
	}
}

func TestExecuteToolValidatesArguments(t *testing.T) {
	if _, err := executeTool("add_numbers", `{"num1": 47}`); err == nil {
		t.Error("missing num2 should be rejected")
	}
	if _, err := executeTool("add_numbers", `{"num1": 1, "num2": 2, "evil": true}`); err == nil {
		t.Error("unknown fields should be rejected")
	}
	if _, err := executeTool("drop_tables", `{"num1": 1, "num2": 2}`); err == nil {
		t.Error("unknown tool names should be rejected")
	}
	result, err := executeTool("multiply_numbers", `{"num1": 7, "num2": 6}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "42" {
		t.Errorf("result = %q, want 42", result)
	}
}
