package analyze

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/dialect"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/openrouter"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/runner"
)

func TestDiffJSON(t *testing.T) {
	tests := []struct {
		name      string
		a, b      string
		wantPaths []string
	}{
		{"equal reordered", `{"a":1,"b":2}`, `{"b":2,"a":1}`, nil},
		{"changed scalar", `{"a":1}`, `{"a":2}`, []string{"$.a"}},
		{"only in a", `{"a":1,"x":true}`, `{"a":1}`, []string{"$.x"}},
		{"only in b", `{"a":1}`, `{"a":1,"y":"z"}`, []string{"$.y"}},
		{"nested", `{"o":{"k":[1,2]}}`, `{"o":{"k":[1,3,4]}}`, []string{"$.o.k[1]", "$.o.k[2]"}},
		{"type change", `{"a":1}`, `{"a":"1"}`, []string{"$.a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs, err := DiffJSON([]byte(tt.a), []byte(tt.b))
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, d := range diffs {
				paths = append(paths, d.Path)
			}
			if strings.Join(paths, " ") != strings.Join(tt.wantPaths, " ") {
				t.Errorf("paths = %v, want %v", paths, tt.wantPaths)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	if got := Classify([]byte(`{"a":1}`), []byte(`{"a":1}`)); got != "identical" {
		t.Errorf("identical = %q", got)
	}
	if got := Classify([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)); got != "equivalent" {
		t.Errorf("equivalent = %q", got)
	}
	if got := Classify([]byte(`{"a":1}`), []byte(`{"a":2}`)); got != "divergent" {
		t.Errorf("divergent = %q", got)
	}
}

func TestFindKeyRecursive(t *testing.T) {
	doc := []byte(`{"generationConfig":{"topK":40,"maxOutputTokens":64}}`)
	key, path, found := findKey(doc, []string{"top_k", "topK"})
	if !found || key != "topK" || path != "$.generationConfig.topK" {
		t.Errorf("found=%v key=%q path=%q", found, key, path)
	}
	if _, _, found := findKey(doc, []string{"seed"}); found {
		t.Error("seed should not be found")
	}
}

func iptr(v int) *int         { return &v }
func fptr(v float64) *float64 { return &v }

func syntheticRun() *runner.RunData {
	upChat := json.RawMessage(`{"model":"up","temperature":1.97,"top_k":40,"seed":999331,"max_tokens":32}`)
	upResp := json.RawMessage(`{"model":"up","temperature":1.97,"seed":999331,"max_output_tokens":32}`)
	res := func(up json.RawMessage) *dialect.Result {
		r := &dialect.Result{
			Text:         "ok",
			StopReason:   "stop",
			GenerationID: "gen-1",
			Usage:        dialect.Usage{PromptTokens: iptr(10), CompletionTokens: iptr(2), Cost: fptr(0.0001)},
			Reconciled: &openrouter.GenerationRecord{
				ID: "gen-1", ProviderName: "Prov",
				NativeTokensPrompt: iptr(11), NativeTokensCompletion: iptr(2),
				TokensPrompt: iptr(12), TotalCost: fptr(0.00012),
			},
			RouterMetadata: json.RawMessage(`{"strategy":"direct"}`),
		}
		if up != nil {
			r.UpstreamBodies = []json.RawMessage{up}
		}
		return r
	}
	reqBody := json.RawMessage(`{"model":"m","temperature":1.97,"top_k":40,"seed":999331,"max_completion_tokens":32}`)
	msgReq := json.RawMessage(`{"model":"m","temperature":1.97,"top_k":40,"max_tokens":32}`)

	return &runner.RunData{
		RunID:     "test-run",
		StartedAt: time.Now(), FinishedAt: time.Now().Add(time.Minute),
		Models: []string{"x-ai/grok-4.6"},
		Catalog: map[string]openrouter.Model{
			"x-ai/grok-4.6": {ID: "x-ai/grok-4.6", SupportedParameters: []string{"temperature", "seed"}},
		},
		Outcomes: []runner.Outcome{
			{
				EntryID: "param_fate", Kind: "param_fate", Model: "x-ai/grok-4.6", Dialect: "chat",
				Attempt: 1, Streamed: true, Status: 200, RequestBody: reqBody,
				Result: res(upChat),
				Events: []dialect.StreamEvent{{Type: "", Data: json.RawMessage(`{"choices":[]}`)}},
			},
			{
				EntryID: "param_fate", Kind: "param_fate", Model: "x-ai/grok-4.6", Dialect: "responses",
				Attempt: 1, Streamed: true, Status: 200, RequestBody: reqBody,
				Result: res(upResp),
				Events: []dialect.StreamEvent{{Type: "response.debug", Data: json.RawMessage(`{"type":"response.debug"}`)}},
			},
			{
				EntryID: "param_fate", Kind: "param_fate", Model: "x-ai/grok-4.6", Dialect: "messages",
				Attempt: 1, Streamed: true, Status: 200, RequestBody: msgReq,
				Notes:  dialect.BuildNotes{Unexpressible: map[string]string{"Seed": "no seed field"}},
				Result: res(nil),
			},
			{
				EntryID: "plain", Kind: "plain", Model: "x-ai/grok-4.6", Dialect: "chat",
				Attempt: 1, Streamed: true, Status: 200,
				RequestBody: json.RawMessage(`{"model":"m"}`), Result: res(upChat),
			},
			{
				EntryID: "buffered", Kind: "buffered", Model: "x-ai/grok-4.6", Dialect: "chat",
				Attempt: 1, Streamed: false, Status: 200,
				RequestBody: json.RawMessage(`{"model":"m"}`), Result: res(nil),
			},
			{
				EntryID: "err_bad_model", Kind: "error_probe", Model: "orlab/does-not-exist", Dialect: "chat",
				Attempt: 1, Status: 404, RequestBody: json.RawMessage(`{"model":"orlab/does-not-exist"}`),
				APIError: &openrouter.APIError{Status: 404, ErrorType: "not_found", Raw: json.RawMessage(`{"error":{}}`)},
			},
			{
				EntryID: "err_bad_model", Kind: "error_probe", Model: "orlab/does-not-exist", Dialect: "messages",
				Attempt: 1, Status: 400, RequestBody: json.RawMessage(`{"model":"orlab/does-not-exist"}`),
				APIError: &openrouter.APIError{Status: 400, ErrorType: "invalid_request", Raw: json.RawMessage(`{"error":{}}`)},
			},
		},
	}
}

func TestWriteReports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	if err := WriteReports(dir, syntheticRun(), "runs/test/transcript.txt"); err != nil {
		t.Fatal(err)
	}

	analysis := readFile(t, filepath.Join(dir, "ANALYSIS.md"))
	for _, want := range []string{
		// drift: chat vs responses upstream bodies differ
		"divergent",
		"$.top_k",
		// param fates
		"| top_k |",
		"dropped",       // responses upstream lacks top_k
		"unexpressible", // messages seed
		"passed",
		// max_tokens renamed across upstreams
		"renamed(",
		// taxonomy with the inconsistent error_type pair
		"not_found",
		"invalid_request",
		"1 inconsistent",
		// ledger flags the inline/reconciled disagreement
		"inline prompt 10 ≠ reconciled 12",
		// buffered comparison
		"as documented",
		// findings
		"echo without streaming",
	} {
		if !strings.Contains(analysis, want) {
			t.Errorf("ANALYSIS.md missing %q", want)
		}
	}

	matrix := readFile(t, filepath.Join(dir, "CAPABILITY-MATRIX.md"))
	for _, want := range []string{"| param_fate |", "✅ 1/1", "❌ 404 not_found ×1"} {
		if !strings.Contains(matrix, want) {
			t.Errorf("CAPABILITY-MATRIX.md missing %q", want)
		}
	}

	grammar := readFile(t, filepath.Join(dir, "STREAMING-GRAMMAR.md"))
	for _, want := range []string{"(untyped data chunk)", "response.debug"} {
		if !strings.Contains(grammar, want) {
			t.Errorf("STREAMING-GRAMMAR.md missing %q", want)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
