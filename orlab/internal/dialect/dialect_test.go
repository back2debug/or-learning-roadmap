package dialect

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func ptr[T any](v T) *T { return &v }

// richSpec exercises every canonical field at once so the build goldens act
// as the cross-dialect translation reference.
func richSpec() PromptSpec {
	return PromptSpec{
		ID:     "golden",
		Model:  "x-ai/grok-4.6",
		Kind:   "tools",
		System: "You are terse.",
		Turns: []Turn{
			{Role: RoleUser, Text: "What's the weather in Oslo?"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`),
			}}},
			{Role: RoleUser, ToolCallID: "call_1", Text: `{"temp_c":7}`},
			{Role: RoleUser, Text: "Summarize."},
		},
		Tools: []ToolDef{{
			Name:        "get_weather",
			Description: "Current weather for a city",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
		ToolChoice:      ToolChoice{Mode: "auto"},
		MaxTokens:       128,
		Temperature:     ptr(0.0),
		TopP:            ptr(0.9),
		TopK:            ptr(40),
		StopSequences:   []string{"END"},
		Seed:            ptr(1234),
		ReasoningEffort: "low",
		Stream:          true,
		SessionID:       "orlab-golden-session",
		Metadata:        map[string]string{"run": "golden"},
		EchoUpstream:    true,
	}
}

func TestBuildRequestGolden(t *testing.T) {
	for _, d := range []Dialect{Chat{}, Messages{}, Responses{}} {
		t.Run(d.Name(), func(t *testing.T) {
			req, notes, err := d.BuildRequest(richSpec())
			if err != nil {
				t.Fatal(err)
			}
			artifact := map[string]any{
				"path":  req.Path,
				"body":  json.RawMessage(req.Body),
				"notes": notes,
			}
			got, err := json.MarshalIndent(artifact, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')

			golden := filepath.Join("testdata", "golden", "build_"+d.Name()+".json")
			if *update {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("build output drifted from %s\ngot:\n%s\nwant:\n%s", golden, got, want)
			}
		})
	}
}

func TestBuildDeterministic(t *testing.T) {
	for _, d := range []Dialect{Chat{}, Messages{}, Responses{}} {
		a, _, err := d.BuildRequest(richSpec())
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := d.BuildRequest(richSpec())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Body, b.Body) {
			t.Errorf("%s: identical specs produced different bodies", d.Name())
		}
	}
}

func streamFixture(t *testing.T, name string) StreamReader {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return NewStreamReader(f, time.Now())
}

func TestChatParseStream(t *testing.T) {
	res, events, err := Chat{}.ParseStream(streamFixture(t, "chat_stream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello world" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.GenerationID != "gen-chat-001" || res.Provider != "xAI" {
		t.Errorf("id=%q provider=%q", res.GenerationID, res.Provider)
	}
	if res.StopReason != "stop" || res.NativeStopReason != "completed" {
		t.Errorf("stop=%q native=%q", res.StopReason, res.NativeStopReason)
	}
	if len(res.UpstreamBodies) != 1 {
		t.Fatalf("UpstreamBodies = %d, want 1", len(res.UpstreamBodies))
	}
	var up struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(res.UpstreamBodies[0], &up); err != nil || up.Model != "grok-4.6" {
		t.Errorf("upstream body = %s (%v)", res.UpstreamBodies[0], err)
	}
	if res.RouterMetadata == nil {
		t.Error("RouterMetadata missing")
	}
	if got := res.Usage; got.PromptTokens == nil || *got.PromptTokens != 10 ||
		got.CompletionTokens == nil || *got.CompletionTokens != 2 ||
		got.CachedTokens == nil || *got.CachedTokens != 3 ||
		got.Cost == nil || *got.Cost != 0.00004 {
		t.Errorf("Usage = %+v", got)
	}
	// 4 data chunks (comment + [DONE] excluded).
	if len(events) != 4 {
		t.Errorf("events = %d, want 4", len(events))
	}
}

func TestChatParseStreamToolCalls(t *testing.T) {
	res, _, err := Chat{}.ParseStream(streamFixture(t, "chat_stream_tools.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"Oslo"}` {
		t.Errorf("ToolCall = %+v", tc)
	}
}

func TestChatParseResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "chat_buffered.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Chat{}.ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hi there" || res.StopReason != "tool_use" || len(res.ToolCalls) != 1 {
		t.Errorf("res = %+v", res)
	}
	if _, ok := res.Unsupported["UpstreamBodies"]; !ok {
		t.Error("buffered result must mark UpstreamBodies unsupported")
	}
}

func TestMessagesParseStream(t *testing.T) {
	res, events, err := Messages{}.ParseStream(streamFixture(t, "messages_stream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello world" || res.ReasoningText != "hmm" {
		t.Errorf("text=%q reasoning=%q", res.Text, res.ReasoningText)
	}
	if res.GenerationID != "msg_01" {
		t.Errorf("id = %q", res.GenerationID)
	}
	if res.StopReason != "stop" || res.NativeStopReason != "end_turn" {
		t.Errorf("stop=%q native=%q", res.StopReason, res.NativeStopReason)
	}
	if res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 12 ||
		res.Usage.CompletionTokens == nil || *res.Usage.CompletionTokens != 5 ||
		res.Usage.Cost == nil || *res.Usage.Cost != 0.00003 {
		t.Errorf("Usage = %+v", res.Usage)
	}
	if res.RouterMetadata == nil {
		t.Error("RouterMetadata missing (should come from message_stop)")
	}
	if len(res.UpstreamBodies) != 0 {
		t.Errorf("UpstreamBodies = %d, want 0 on messages", len(res.UpstreamBodies))
	}
	if _, ok := res.Unsupported["Provider"]; !ok {
		t.Error("messages must mark Provider unsupported")
	}
	// Every event carries its SSE type for the grammar report, and every
	// payload must be valid JSON or the whole run record fails to marshal.
	for _, ev := range events {
		if ev.Type == "" {
			t.Errorf("untyped event: %s", ev.Data)
		}
		if !json.Valid(ev.Data) {
			t.Errorf("event %q has non-JSON payload: %s", ev.Type, ev.Data)
		}
	}
	// OpenRouter appends an OpenAI-style "data: [DONE]" terminator to the
	// Anthropic grammar; it must terminate the stream, not be parsed as an
	// event or recorded as one.
	for _, ev := range events {
		if string(ev.Data) == "[DONE]" || ev.Type == "data" {
			t.Errorf("[DONE] sentinel leaked into the event log: %+v", ev)
		}
	}
}

func TestMessagesParseStreamToolUse(t *testing.T) {
	res, _, err := Messages{}.ParseStream(streamFixture(t, "messages_stream_tools.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "tool_use" || len(res.ToolCalls) != 1 {
		t.Fatalf("stop=%q tools=%d", res.StopReason, len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"Oslo"}` {
		t.Errorf("ToolCall = %+v", tc)
	}
}

func TestMessagesParseResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "messages_buffered.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Messages{}.ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hi there" || res.ReasoningText != "considering" {
		t.Errorf("text=%q reasoning=%q", res.Text, res.ReasoningText)
	}
	if res.StopReason != "length" || res.NativeStopReason != "max_tokens" {
		t.Errorf("stop=%q native=%q", res.StopReason, res.NativeStopReason)
	}
	if res.Usage.CachedTokens == nil || *res.Usage.CachedTokens != 2 {
		t.Errorf("CachedTokens = %v", res.Usage.CachedTokens)
	}
}

func TestResponsesParseStream(t *testing.T) {
	res, events, err := Responses{}.ParseStream(streamFixture(t, "responses_stream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello world" {
		t.Errorf("Text = %q (terminal snapshot must not double-count deltas)", res.Text)
	}
	if res.GenerationID != "resp_01" || res.Provider != "OpenAI" {
		t.Errorf("id=%q provider=%q", res.GenerationID, res.Provider)
	}
	if res.StopReason != "stop" || res.NativeStopReason != "completed" {
		t.Errorf("stop=%q native=%q", res.StopReason, res.NativeStopReason)
	}
	if len(res.UpstreamBodies) != 1 {
		t.Fatalf("UpstreamBodies = %d, want 1", len(res.UpstreamBodies))
	}
	var up struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(res.UpstreamBodies[0], &up); err != nil || up.Model != "o4-mini" {
		t.Errorf("upstream body = %s (%v)", res.UpstreamBodies[0], err)
	}
	if res.RouterMetadata == nil {
		t.Error("RouterMetadata missing")
	}
	if len(events) != 7 {
		t.Errorf("events = %d, want 7", len(events))
	}
}

func TestResponsesParseResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "responses_buffered.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Responses{}.ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Partial" || res.ReasoningText != "thought" {
		t.Errorf("text=%q reasoning=%q", res.Text, res.ReasoningText)
	}
	if res.StopReason != "length" || res.NativeStopReason != "incomplete:max_output_tokens" {
		t.Errorf("stop=%q native=%q", res.StopReason, res.NativeStopReason)
	}
	if res.Usage.ReasoningTokens == nil || *res.Usage.ReasoningTokens != 32 {
		t.Errorf("ReasoningTokens = %v", res.Usage.ReasoningTokens)
	}
}

func TestMessagesOmitsMaxTokensWhenZero(t *testing.T) {
	spec := PromptSpec{Model: "m", Turns: []Turn{{Role: RoleUser, Text: "hi"}}}
	req, _, err := Messages{}.BuildRequest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(req.Body, []byte("max_tokens")) {
		t.Errorf("error_probe needs max_tokens omitted when unset: %s", req.Body)
	}
}

func TestSeedFateDiverges(t *testing.T) {
	spec := PromptSpec{Model: "m", Seed: ptr(7), Turns: []Turn{{Role: RoleUser, Text: "hi"}}}
	if req, notes, _ := (Chat{}).BuildRequest(spec); !bytes.Contains(req.Body, []byte(`"seed":7`)) || len(notes.Unexpressible) != 0 {
		t.Errorf("chat should express seed natively: %s %v", req.Body, notes.Unexpressible)
	}
	if req, notes, _ := (Messages{}).BuildRequest(spec); bytes.Contains(req.Body, []byte("seed")) || notes.Unexpressible["Seed"] == "" {
		t.Errorf("messages should mark seed unexpressible: %s %v", req.Body, notes.Unexpressible)
	}
	if req, notes, _ := (Responses{}).BuildRequest(spec); !bytes.Contains(req.Body, []byte(`"seed":7`)) || notes.Transformed["Seed"] == "" {
		t.Errorf("responses should send seed as a probe with a note: %s %v", req.Body, notes.Transformed)
	}
}
