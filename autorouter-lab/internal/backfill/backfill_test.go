package backfill

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/logging"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/transport"
)

const trialLog = `{"msg":"trial","run_id":"r1","trial_id":"t1","cell_id":"a/auto","generation_id":"gen-1","status":200}
{"msg":"trial","run_id":"r1","trial_id":"t2","cell_id":"a/auto","generation_id":"gen-2","status":200}
{"msg":"trial","run_id":"r1","trial_id":"t3","cell_id":"b/auto","generation_id":"gen-404","status":404}
{"msg":"trial","run_id":"r1","trial_id":"t4","cell_id":"b/auto","generation_id":"","status":200}
{"msg":"trial","run_id":"r1","trial_id":"t5","cell_id":"b/auto","generation_id":"gen-dry","status":200,"dry_run":true}
{"msg":"trial","run_id":"r2","trial_id":"t6","cell_id":"a/auto","generation_id":"gen-3","status":200}
{"msg":"trial","run_id":"r2","trial_id":"t7","cell_id":"a/auto","generation_id":"gen-3","status":200}
not json
{"msg":"run finished","run_id":"r2","generation_id":"gen-x","status":200}
`

func ids(ts []Target) string {
	var out []string
	for _, t := range ts {
		out = append(out, t.GenerationID)
	}
	return strings.Join(out, ",")
}

func TestTargets(t *testing.T) {
	tests := []struct {
		name  string
		runID string
		done  map[string]bool
		want  string
	}{
		{"all runs", "", nil, "gen-1,gen-2,gen-3"},
		{"one run", "r2", nil, "gen-3"},
		{"skip done", "", map[string]bool{"gen-1": true, "gen-3": true}, "gen-2"},
	}
	for _, tc := range tests {
		got, err := Targets(strings.NewReader(trialLog), tc.runID, tc.done)
		if err != nil {
			t.Fatal(err)
		}
		if ids(got) != tc.want {
			t.Errorf("%s: targets = %s, want %s", tc.name, ids(got), tc.want)
		}
	}
}

func TestDoneCountsOnlySuccesses(t *testing.T) {
	done, err := Done(strings.NewReader(`{"msg":"generation","generation_id":"gen-1","status":200}
{"msg":"generation","generation_id":"gen-2","status":404}
{"msg":"generation","generation_id":"gen-3","status":0}
junk
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || !done["gen-1"] {
		t.Errorf("done = %v, want only gen-1 so the others are retried", done)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func TestRun(t *testing.T) {
	const key = "sk-or-v1-testtesttesttesttesttest"
	t.Setenv(config.APIKeyEnv, key)
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		mu.Lock()
		hits[id]++
		n := hits[id]
		mu.Unlock()
		if r.URL.Path != "/generation" || r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case id == "gen-missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
		case id == "gen-flaky" && n == 1:
			w.WriteHeader(http.StatusBadGateway)
		case id == "gen-down":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = io.WriteString(w, `{"data":{"id":"`+id+`","total_cost":0.00042,"native_tokens_prompt":51,"future_field":true}}`)
		}
	}))
	t.Cleanup(srv.Close)

	out := &syncBuf{}
	targets := []Target{
		{GenerationID: "gen-ok", RunID: "r1", TrialID: "t1", CellID: "a/auto"},
		{GenerationID: "gen-missing"},
		{GenerationID: "gen-flaky"},
		{GenerationID: "gen-down"},
	}
	sum, err := Run(context.Background(), targets, Options{
		BaseURL: srv.URL,
		Client:  transport.NewHTTPClient(false, transport.DefaultGuards()),
		Out:     logging.NewJSON(out, logging.Options{Secrets: []string{key}}),
		Sleep:   func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum != (Summary{Fetched: 2, Missing: 1, Failed: 1}) {
		t.Errorf("summary = %+v, want 2 fetched, 1 missing, 1 failed", sum)
	}
	// 4xx is asked once; 5xx is retried up to the bound and no further.
	if hits["gen-missing"] != 1 || hits["gen-flaky"] != 2 || hits["gen-down"] != maxAttempts {
		t.Errorf("hits = %v", hits)
	}
	if strings.Contains(out.b.String(), key) {
		t.Fatal("the key reached the backfill file")
	}
	recs := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.b.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad record %q", line)
		}
		recs[m["generation_id"].(string)] = m
	}
	ok := recs["gen-ok"]
	gen, _ := ok["generation"].(map[string]any)
	if ok["status"] != 200.0 || ok["cell_id"] != "a/auto" || gen["total_cost"] != 0.00042 || gen["future_field"] != true {
		t.Errorf("gen-ok record = %v", ok)
	}
	// What was written must make a re-run skip only the successes.
	done, err := Done(strings.NewReader(out.b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 || !done["gen-ok"] || !done["gen-flaky"] {
		t.Errorf("done after run = %v", done)
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	t.Setenv(config.APIKeyEnv, "sk-or-v1-testtesttesttesttesttest")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, []Target{{GenerationID: "a"}, {GenerationID: "b"}}, Options{
		BaseURL: srv.URL, Client: transport.NewHTTPClient(false, transport.DefaultGuards()),
		Out: logging.NewJSON(io.Discard, logging.Options{}),
	})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v, want a prompt cancellation", err, time.Since(start))
	}
}
