package record

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeKey = "sk-or-v1-testsecret123"

func TestWriterRedactsAndFilePerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	w, err := NewWriter(dir, "run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Write("exchange", map[string]string{"auth": "Bearer " + fakeKey}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("sk-or-")) {
		t.Fatalf("key leaked to disk: %s", b)
	}
	if !bytes.Contains(b, []byte("[REDACTED]")) {
		t.Fatalf("expected redaction placeholder: %s", b)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestRecordThenReplayRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	w, err := NewWriter(dir, "run.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"model":"m","stream":true}`)
	ex := Exchange{
		Key:         ExchangeKey("POST", "/api/v1/chat/completions", body),
		Method:      "POST",
		Path:        "/api/v1/chat/completions",
		RequestBody: string(body),
		Status:      200,
		RespHeaders: map[string][]string{"Content-Type": {"text/event-stream"}},
		RespBody:    "data: {\"id\":\"gen-1\"}\n\ndata: [DONE]\n",
	}
	if err := w.Write("exchange", ex); err != nil {
		t.Fatal(err)
	}
	// Non-exchange lines must be ignored by the replayer.
	if err := w.Write("phase", map[string]string{"phase": "build"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	rp, err := LoadReplayer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rp.Len() != 1 {
		t.Fatalf("Len = %d, want 1", rp.Len())
	}

	req, _ := http.NewRequestWithContext(context.Background(), "POST",
		"https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	resp, err := rp.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != ex.RespBody {
		t.Errorf("status=%d body=%q", resp.StatusCode, got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	// A request with a different body must miss.
	req2, _ := http.NewRequestWithContext(context.Background(), "POST",
		"https://openrouter.ai/api/v1/chat/completions", strings.NewReader(`{"other":1}`))
	if _, err := rp.Do(req2); err == nil {
		t.Error("expected cassette miss for unrecorded body")
	}
}

func TestCaptureBodyFiresOnceWithAllBytes(t *testing.T) {
	var captured []byte
	calls := 0
	rc := CaptureBody(io.NopCloser(strings.NewReader("hello stream")), func(b []byte) {
		captured = append([]byte(nil), b...)
		calls++
	})
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close() // close after EOF must not re-fire
	if string(got) != "hello stream" || string(captured) != "hello stream" || calls != 1 {
		t.Errorf("got=%q captured=%q calls=%d", got, captured, calls)
	}
}

func TestCaptureBodyFiresOnEarlyClose(t *testing.T) {
	var captured []byte
	rc := CaptureBody(io.NopCloser(strings.NewReader("abcdef")), func(b []byte) {
		captured = append([]byte(nil), b...)
	})
	buf := make([]byte, 3)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if string(captured) != "abc" {
		t.Errorf("captured = %q, want partial read", captured)
	}
}
