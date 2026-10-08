package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/config"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/dialect"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/openrouter"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/record"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/transport"
)

// fakeOpenRouter serves all routes the runner touches.
func fakeOpenRouter(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[
			{"id":"test/model","name":"Test Model","context_length":8192,"supported_parameters":["temperature","top_p","seed"]},
			{"id":"test/model:free","name":"Test Model Free","context_length":8192,"supported_parameters":["temperature"]}
		]}`)
	})
	mux.HandleFunc("GET /api/v1/generation", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		fmt.Fprintf(w, `{"data":{"id":%q,"model":"test/model","provider_name":"FakeProv",
			"total_cost":0.00005,"native_tokens_prompt":11,"native_tokens_completion":3,
			"finish_reason":"stop","native_finish_reason":"stop"}}`, id)
	})
	sse := func(w http.ResponseWriter, id string) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":%q,\"choices\":[],\"debug\":{\"echo_upstream_body\":{\"model\":\"upstream-model\",\"max_tokens\":64}}}\n\n", id)
		fmt.Fprintf(w, "data: {\"id\":%q,\"provider\":\"FakeProv\",\"choices\":[{\"delta\":{\"content\":\"hello from orlab\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"cost\":0.00004},\"openrouter_metadata\":{\"strategy\":\"direct\",\"attempt\":1}}\n\n", id)
		io.WriteString(w, "data: [DONE]\n\n")
	}
	mux.HandleFunc("POST /api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("does-not-exist")) {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"model not found","metadata":{"error_type":"not_found"}}}`)
			return
		}
		if bytes.Contains(body, []byte(`"stream":true`)) {
			sse(w, "gen-chat-1")
			return
		}
		io.WriteString(w, `{"id":"gen-chat-buf","provider":"FakeProv","choices":[{"message":{"content":"hello from orlab"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`)
	})
	mux.HandleFunc("POST /api/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("does-not-exist")) {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"not_found","message":"no model","metadata":{"error_type":"not_found"}}}`)
			return
		}
		if !bytes.Contains(body, []byte(`"stream":true`)) {
			io.WriteString(w, `{"id":"resp-buf","status":"completed","provider":"FakeProv","output":[{"type":"message","content":[{"type":"output_text","text":"hello from orlab"}]}],"usage":{"input_tokens":10,"output_tokens":4}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.debug\ndata: {\"type\":\"response.debug\",\"debug\":{\"upstream_body\":{\"model\":\"upstream-model\"}}}\n\n")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"provider\":\"FakeProv\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from orlab\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}},\"openrouter_metadata\":{\"strategy\":\"direct\"}}\n\n")
	})
	mux.HandleFunc("POST /api/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("does-not-exist")) {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"not found","metadata":{"error_type":"not_found"}}}`)
			return
		}
		if !bytes.Contains(body, []byte(`"stream":true`)) {
			io.WriteString(w, `{"id":"msg-buf","model":"test/model","content":[{"type":"text","text":"hello from orlab"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"usage\":{\"input_tokens\":10}}}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from orlab\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\",\"openrouter_metadata\":{\"strategy\":\"direct\"}}\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testSuite() []dialect.PromptSpec {
	return []dialect.PromptSpec{
		{ID: "plain", Kind: "plain", Turns: []dialect.Turn{{Role: dialect.RoleUser, Text: "Reply with exactly: hello from orlab"}}, MaxTokens: 64},
		{ID: "buffered", Kind: "buffered", Turns: []dialect.Turn{{Role: dialect.RoleUser, Text: "Reply with exactly: hello from orlab"}}, MaxTokens: 64},
		{ID: "err_bad_model", Kind: "error_probe", Model: "orlab/does-not-exist", Turns: []dialect.Turn{{Role: dialect.RoleUser, Text: "hello"}}, MaxTokens: 32},
	}
}

func newTestRunner(t *testing.T, baseURL, runDir string, doer Doer, replay bool) (*Runner, *record.Writer) {
	t.Helper()
	rec, err := record.NewWriter(runDir, "run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rec.Close() })
	cfg := &config.Config{
		APIKey: "sk-or-v1-test", BaseURL: baseURL,
		Stream: true, EchoUpstream: true,
		Concurrency: 2, Timeout: 10 * time.Second, Repeat: 1,
	}
	orc := &openrouter.Client{
		Do: doer, BaseURL: baseURL, APIKey: cfg.APIKey,
		ReconcileDelays: []time.Duration{time.Millisecond},
	}
	return &Runner{
		Cfg: cfg, Do: doer, OR: orc,
		Dialects:   []dialect.Dialect{dialect.Chat{}, dialect.Responses{}, dialect.Messages{}},
		Rec:        rec,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Transcript: &bytes.Buffer{},
		Replay:     replay,
	}, rec
}

func TestRunEndToEnd(t *testing.T) {
	srv := fakeOpenRouter(t)
	base := srv.URL + "/api/v1"
	tc, err := transport.New(transport.Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(t.TempDir(), "run1")
	rec, _ := record.NewWriter(runDir, "run.jsonl")
	doer := &record.RecordingDoer{Inner: tc, W: rec}
	r, _ := newTestRunner(t, base, filepath.Join(t.TempDir(), "meta"), doer, false)

	data, err := r.Run(context.Background(), testSuite(), []string{"test/model"})
	if err != nil {
		t.Fatal(err)
	}
	rec.Close()

	// 2 normal entries × 1 model × 3 dialects + 1 pinned × 3 = 9 outcomes.
	if len(data.Outcomes) != 9 {
		t.Fatalf("outcomes = %d, want 9", len(data.Outcomes))
	}
	if data.Partial {
		t.Error("run marked partial")
	}
	if _, ok := data.Catalog["test/model"]; !ok {
		t.Error("catalog missing test/model")
	}

	byKey := map[string]Outcome{}
	for _, o := range data.Outcomes {
		byKey[o.EntryID+"/"+o.Dialect] = o
	}

	// Streamed chat outcome: echo, metadata, reconciliation all present.
	chat := byKey["plain/chat"]
	if chat.Result == nil || chat.Result.Text != "hello from orlab" {
		t.Fatalf("chat result = %+v", chat.Result)
	}
	if len(chat.Result.UpstreamBodies) != 1 {
		t.Errorf("chat upstream bodies = %d", len(chat.Result.UpstreamBodies))
	}
	if chat.Result.RouterMetadata == nil {
		t.Error("chat router metadata missing")
	}
	if chat.Result.Reconciled == nil || chat.Result.Reconciled.ProviderName != "FakeProv" {
		t.Errorf("chat reconciled = %+v", chat.Result.Reconciled)
	}
	if !chat.Streamed {
		t.Error("plain entry should stream")
	}

	// Buffered entry must not stream and must carry no echo data on any
	// dialect — the documented streaming-only constraint.
	for _, d := range []string{"chat", "responses", "messages"} {
		buf := byKey["buffered/"+d]
		if buf.Streamed {
			t.Errorf("%s: buffered entry must not stream", d)
		}
		if buf.Result == nil {
			t.Errorf("%s: buffered entry failed to parse: %s", d, buf.TransportErr)
			continue
		}
		if len(buf.Result.UpstreamBodies) != 0 {
			t.Errorf("%s: buffered result should have no upstream bodies", d)
		}
		if buf.Result.Text != "hello from orlab" {
			t.Errorf("%s: buffered text = %q", d, buf.Result.Text)
		}
		if _, ok := buf.Result.Unsupported["UpstreamBodies"]; !ok {
			t.Errorf("%s: buffered result must mark UpstreamBodies unsupported", d)
		}
	}

	// Error probe: APIError with canonical type on every dialect.
	for _, d := range []string{"chat", "responses", "messages"} {
		e := byKey["err_bad_model/"+d]
		if e.Status != 404 || e.APIError == nil || e.APIError.ErrorType != "not_found" {
			t.Errorf("%s error probe: status=%d apiErr=%+v", d, e.Status, e.APIError)
		}
	}

	// Messages result should carry metadata from message_stop.
	msg := byKey["plain/messages"]
	if msg.Result == nil || msg.Result.RouterMetadata == nil {
		t.Error("messages router metadata missing")
	}

	// The cassette file must exist and contain exchanges but no key.
	raw, err := os.ReadFile(filepath.Join(runDir, "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"type":"exchange"`)) {
		t.Error("no exchange records in cassette")
	}
	if bytes.Contains(raw, []byte("sk-or-")) {
		t.Error("credential leaked into cassette")
	}
}

func TestReplayReproducesRun(t *testing.T) {
	srv := fakeOpenRouter(t)
	base := srv.URL + "/api/v1"
	tc, err := transport.New(transport.Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(t.TempDir(), "rec")
	rec, _ := record.NewWriter(runDir, "run.jsonl")
	doer := &record.RecordingDoer{Inner: tc, W: rec}
	r1, _ := newTestRunner(t, base, filepath.Join(t.TempDir(), "m1"), doer, false)
	live, err := r1.Run(context.Background(), testSuite(), []string{"test/model"})
	if err != nil {
		t.Fatal(err)
	}
	rec.Close()
	srv.Close() // network gone; replay must not need it

	rp, err := record.LoadReplayer(runDir)
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := newTestRunner(t, base, filepath.Join(t.TempDir(), "m2"), rp, true)
	replayed, err := r2.Run(context.Background(), testSuite(), []string{"test/model"})
	if err != nil {
		t.Fatal(err)
	}

	if len(replayed.Outcomes) != len(live.Outcomes) {
		t.Fatalf("replay outcomes = %d, live = %d", len(replayed.Outcomes), len(live.Outcomes))
	}
	for i := range live.Outcomes {
		l, p := live.Outcomes[i], replayed.Outcomes[i]
		if l.EntryID != p.EntryID || l.Dialect != p.Dialect || l.Status != p.Status {
			t.Errorf("outcome %d diverged: live=%s/%s/%d replay=%s/%s/%d",
				i, l.EntryID, l.Dialect, l.Status, p.EntryID, p.Dialect, p.Status)
		}
		if (l.Result == nil) != (p.Result == nil) {
			t.Errorf("outcome %d result presence diverged", i)
			continue
		}
		if l.Result != nil && l.Result.Text != p.Result.Text {
			t.Errorf("outcome %d text diverged: %q vs %q", i, l.Result.Text, p.Result.Text)
		}
	}
}

func TestPreflightRejectsUnknownModelWithSuggestion(t *testing.T) {
	srv := fakeOpenRouter(t)
	base := srv.URL + "/api/v1"
	tc, _ := transport.New(transport.Options{BaseURL: base})
	r, _ := newTestRunner(t, base, t.TempDir(), tc, false)
	_, err := r.Run(context.Background(), testSuite(), []string{"test/modle"})
	if err == nil || !strings.Contains(err.Error(), "test/model") {
		t.Fatalf("err = %v, want unknown-model suggestion", err)
	}
}

func TestCheapProfileSwapsFreeVariant(t *testing.T) {
	srv := fakeOpenRouter(t)
	base := srv.URL + "/api/v1"
	tc, _ := transport.New(transport.Options{BaseURL: base})
	r, _ := newTestRunner(t, base, t.TempDir(), tc, false)
	r.Cfg.Cheap = true
	data, err := r.Run(context.Background(), []dialect.PromptSpec{
		{ID: "plain", Kind: "plain", Turns: []dialect.Turn{{Role: dialect.RoleUser, Text: "hi"}}, MaxTokens: 8},
	}, []string{"test/model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Models) != 1 || data.Models[0] != "test/model:free" {
		t.Errorf("Models = %v, want free variant", data.Models)
	}
}
