package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "sk-or-v1-0123456789abcdef0123456789abcdefWXYZ"

func logOne(t *testing.T, o Options, args ...any) string {
	t.Helper()
	var buf bytes.Buffer
	NewJSON(&buf, o).Info("trial", args...)
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("record is not valid JSON: %s", buf.String())
	}
	return buf.String()
}

func TestRedaction(t *testing.T) {
	headers := slog.GroupValue(
		slog.String("Authorization", "Bearer "+testKey),
		slog.String("X-Api-Key", testKey),
		slog.String("Set-Cookie", "session=abc"),
		slog.String("X-Session-Token", "tok"),
		slog.String("X-Generation-Id", "gen-123"),
		slog.String("Content-Type", "application/json"),
	)
	payload := Payload(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"my secret prompt"}],"plugins":[{"id":"p","cost_quality_tradeoff":0}]}`)
	tests := []struct {
		name    string
		opts    Options
		args    []any
		want    []string
		notWant []string
	}{
		{
			name:    "sensitive header names",
			opts:    Options{Secrets: []string{testKey}},
			args:    []any{"request_headers", headers},
			want:    []string{`"Authorization":"[REDACTED]"`, `"X-Api-Key":"[REDACTED]"`, `"Set-Cookie":"[REDACTED]"`, `"X-Session-Token":"[REDACTED]"`, `"X-Generation-Id":"gen-123"`, `"Content-Type":"application/json"`},
			notWant: []string{testKey, "session=abc", `"tok"`},
		},
		{
			name:    "key literal in an ordinary string",
			opts:    Options{Secrets: []string{testKey}},
			args:    []any{"note", "sent with " + testKey + " today"},
			want:    []string{"sent with [REDACTED] today"},
			notWant: []string{testKey, "WXYZ"},
		},
		{
			name:    "secret-shaped value without knowing the literal",
			opts:    Options{},
			args:    []any{"note", "Authorization: Bearer abcdefghijklmnop and sk-or-v1-zzzzzzzzzzzz"},
			notWant: []string{"abcdefghijklmnop", "zzzzzzzzzzzz"},
		},
		{
			name:    "secret inside an error",
			opts:    Options{Secrets: []string{testKey}},
			args:    []any{"error", errors.New("401 for key " + testKey)},
			want:    []string{"401 for key [REDACTED]"},
			notWant: []string{testKey},
		},
		{
			name:    "secret inside raw JSON and a struct",
			opts:    Options{Secrets: []string{testKey}},
			args:    []any{"usage", json.RawMessage(`{"echo":"` + testKey + `"}`), "obj", struct{ K string }{testKey}},
			want:    []string{`"usage":{"echo":"[REDACTED]"}`, `"obj":{"K":"[REDACTED]"}`},
			notWant: []string{testKey},
		},
		{
			name:    "usage counters are not mistaken for secrets",
			opts:    Options{},
			args:    []any{"prompt_tokens", 12, "max_tokens", 64},
			want:    []string{`"prompt_tokens":12`, `"max_tokens":64`},
			notWant: []string{Redacted},
		},
		{
			name:    "bodies off by default",
			opts:    Options{},
			args:    []any{"response_body", Body(`{"choices":[{"message":{"content":"model said hi"}}]}`)},
			want:    []string{"[elided ", "pass -log-bodies"},
			notWant: []string{"model said hi"},
		},
		{
			name: "bodies on",
			opts: Options{LogBodies: true, Secrets: []string{testKey}},
			args: []any{"response_body", Body(`{"content":"model said hi ` + testKey + `"}`)},
			want: []string{`"response_body":{"content":"model said hi [REDACTED]"}`},
		},
		{
			name:    "payload keeps routing config but elides the prompt",
			opts:    Options{},
			args:    []any{"request_payload", payload},
			want:    []string{`"plugins":[{"cost_quality_tradeoff":0,"id":"p"}]`, `"max_tokens":64`, `"content":"[elided 16 bytes`},
			notWant: []string{"my secret prompt"},
		},
		{
			name: "payload verbatim with bodies on",
			opts: Options{LogBodies: true},
			args: []any{"request_payload", payload},
			want: []string{`"request_payload":` + string(payload)},
		},
		{
			name:    "non-JSON payload is not shown",
			opts:    Options{},
			args:    []any{"request_payload", Payload("not json my secret prompt")},
			notWant: []string{"my secret prompt"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := logOne(t, tc.opts, tc.args...)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in %s", w, out)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("found %q in %s", nw, out)
				}
			}
		})
	}
}

// Named string types, levels and nil pointers must log as plain values.
func TestPlainValuesStayPlain(t *testing.T) {
	type router string
	var none *string
	some := "math " + testKey
	out := logOne(t, Options{Secrets: []string{testKey}}, "router", router("auto"), "none", none, "some", &some)
	for _, w := range []string{`"level":"INFO"`, `"router":"auto"`, `"none":null`, `"some":"math [REDACTED]"`} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in %s", w, out)
		}
	}
	var buf bytes.Buffer
	NewConsole(&buf, Options{}).Info("x", "router", router("auto"))
	if !strings.Contains(buf.String(), "level=INFO") || !strings.Contains(buf.String(), "router=auto") {
		t.Errorf("console output is not plain: %s", buf.String())
	}
}

// The console handler shares the redaction.
func TestConsoleRedacts(t *testing.T) {
	var buf bytes.Buffer
	NewConsole(&buf, Options{Secrets: []string{testKey}}).Info("x", "Authorization", "Bearer "+testKey, "note", testKey)
	if strings.Contains(buf.String(), "0123456789abcdef") {
		t.Fatalf("console leaked the key: %s", buf.String())
	}
}

func TestKeyLast4(t *testing.T) {
	if got := KeyLast4(testKey); got != "...WXYZ" {
		t.Errorf("KeyLast4 = %q", got)
	}
	if got := KeyLast4("abc12xyz"); strings.Contains(got, "xyz") {
		t.Errorf("KeyLast4 leaked a short key: %q", got)
	}
}

func TestOpenLogMode(t *testing.T) {
	dir := t.TempDir()

	fresh := filepath.Join(dir, "logs", "new.jsonl")
	f, err := OpenLog(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("new log mode = %#o, want 0600", info.Mode().Perm())
	}

	// An existing, too-open file keeps its mode through OpenFile, so OpenLog
	// must notice and refuse.
	open := filepath.Join(dir, "open.jsonl")
	if err := os.WriteFile(open, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(open); !errors.Is(err, ErrLogMode) {
		t.Errorf("OpenLog on 0644 file: err = %v, want ErrLogMode", err)
	}

	// Appends rather than truncates.
	for range 2 {
		f, err := OpenLog(fresh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("line\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "line\nline\n" {
		t.Errorf("log content = %q, want two appended lines", b)
	}
}

// Values from API responses are untrusted: escape sequences must not reach the
// terminal, secrets must not reach it either, and long values are cut to fit.
func TestTable(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewTable(&buf, Options{Secrets: []string{testKey}}, []Column{{Key: "cell", Width: 8}, {Key: "model", Width: 12}, {Key: "note", Width: 30}}))
	log.Info("trial", "cell", "a/auto", "model", "evil\x1b[2J\x07model-with-a-long-name", "note", "key "+testKey)
	log.Info("trial", "cell", "b/auto", "model", "v/m", "note", "")
	log.Info("run finished", "spent", "0.01", "line", "two\nlines")
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want header + 2 rows + 1 plain line:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "CELL") || !strings.Contains(lines[0], "MODEL") {
		t.Errorf("header = %q", lines[0])
	}
	if strings.ContainsAny(out, "\x1b\x07") || strings.Contains(out, testKey) {
		t.Errorf("control characters or key reached the console: %q", out)
	}
	if !strings.Contains(lines[1], "evil?[2J?mo~") || !strings.Contains(lines[1], "key [REDACTED]") {
		t.Errorf("row = %q", lines[1])
	}
	if !strings.Contains(lines[3], "run finished") || !strings.Contains(lines[3], "line=two?lines") {
		t.Errorf("plain line = %q", lines[3])
	}
}

func TestEnsureIgnored(t *testing.T) {
	// This package's own source is tracked; the repository's logs/ is ignored.
	err := EnsureIgnored("logging.go")
	if err == nil {
		t.Skip("not inside a git work tree")
	}
	if !errors.Is(err, ErrNotIgnored) {
		t.Errorf("tracked source file: err = %v, want ErrNotIgnored", err)
	}
	if err := EnsureIgnored(filepath.Join("..", "..", "logs", "trials.jsonl")); err != nil {
		t.Errorf("logs/trials.jsonl: %v", err)
	}
}
