package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/logging"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/transport"
)

// fake is a scripted transport: reply decides each attempt's outcome.
type fake struct {
	mu      sync.Mutex
	specs   []transport.TrialSpec
	active  atomic.Int32
	peak    atomic.Int32
	delay   time.Duration
	reply   func(n int, spec transport.TrialSpec) (transport.TrialResult, error)
	perCell map[string]int
}

func (f *fake) Run(ctx context.Context, spec transport.TrialSpec) (transport.TrialResult, error) {
	cur := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		p := f.peak.Load()
		if cur <= p || f.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return transport.TrialResult{}, transport.ErrNoResponse
		}
	}
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	if f.perCell == nil {
		f.perCell = map[string]int{}
	}
	f.perCell[spec.TrialID]++
	n := f.perCell[spec.TrialID]
	f.mu.Unlock()
	return f.reply(n, spec)
}

func ok(cost float64) (transport.TrialResult, error) {
	return transport.TrialResult{Status: 200, Model: "v/m", CostUSD: &cost, Exchange: &transport.Exchange{}}, nil
}

func status(code int, header http.Header) (transport.TrialResult, error) {
	return transport.TrialResult{Status: code, Exchange: &transport.Exchange{ResponseHeader: header}}, nil
}

func cells(t *testing.T, yaml string) []config.Cell {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := writeFile(p, body); err != nil {
			t.Fatal(err)
		}
		return p
	}
	prompts := write("p.yaml", "version: 1\nprompts:\n  - {id: p1, text: one}\n  - {id: p2, text: two}\n")
	out, err := config.Load(write("e.yaml", yaml), prompts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type harness struct {
	r     *Runner
	f     *fake
	log   *bytes.Buffer
	slept []time.Duration
	mu    sync.Mutex
}

func newHarness(t *testing.T, f *fake, maxSpend float64, maxTrials int) *harness {
	t.Helper()
	h := &harness{f: f, log: &bytes.Buffer{}}
	var err error
	h.r, err = New(
		map[config.TransportKind]transport.Transport{config.TransportChatCompletions: f},
		Options{
			RunID: "run1", MaxSpendUSD: maxSpend, MaxTrials: maxTrials,
			Estimate:    func(Trial) float64 { return 0.5 },
			MaxAttempts: 3, BackoffBase: time.Second, BackoffMax: 4 * time.Second,
			Sleep: func(_ context.Context, d time.Duration) error {
				h.mu.Lock()
				h.slept = append(h.slept, d)
				h.mu.Unlock()
				return nil
			},
			Trials:  logging.NewJSON(&syncWriter{w: h.log}, logging.Options{}),
			Console: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.log.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

const isolate5 = "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, prompt_id: p1, repeats: 5}\n"

func TestBuildPlan(t *testing.T) {
	plan := BuildPlan(cells(t, `
version: 1
experiments:
  - {id: iso, router: both, transport: chat_completions, prompt_id: p1, repeats: 2}
  - {id: st, router: auto, transport: chat_completions, stickiness: sticky, prompt_sequence: [p1, p2], repeats: 2}
  - {id: sw1, router: auto, transport: chat_completions, stickiness: sticky, session_id: shared, prompt_id: p1}
  - {id: sw2, router: auto-beta, transport: chat_completions, stickiness: sticky, session_id: shared, prompt_id: p1}
  - {id: im1, router: auto, transport: chat_completions, stickiness: implicit, prompt_id: p1}
  - {id: im2, router: auto-beta, transport: chat_completions, stickiness: implicit, prompt_id: p2}
`))
	if plan.Trials != 4+4+2+2 {
		t.Fatalf("trials = %d, want 12", plan.Trials)
	}
	var got []string
	for _, seq := range plan.Sequences {
		var ids []string
		for _, tr := range seq {
			ids = append(ids, tr.Cell.ID+":"+tr.Prompt.ID)
		}
		got = append(got, strings.Join(ids, " "))
	}
	want := []string{
		"iso/auto:p1", "iso/auto:p1", "iso/auto-beta:p1", "iso/auto-beta:p1",
		"st/auto:p1 st/auto:p2 st/auto:p1 st/auto:p2",
		"sw1/auto:p1 sw2/auto-beta:p1",
		"im1/auto:p1 im2/auto-beta:p2",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sequences:\n got  %q\n want %q", got, want)
	}
}

func TestSessionIDs(t *testing.T) {
	f := &fake{reply: func(int, transport.TrialSpec) (transport.TrialResult, error) { return ok(0) }}
	h := newHarness(t, f, 1, 100)
	plan := BuildPlan(cells(t, `
version: 1
experiments:
  - {id: iso, router: auto, transport: chat_completions, prompt_id: p1, repeats: 3}
  - {id: st, router: auto, transport: chat_completions, stickiness: sticky, prompt_id: p1, repeats: 3}
  - {id: im, router: auto, transport: chat_completions, stickiness: implicit, prompt_id: p1, repeats: 2}
`))
	if _, err := h.r.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	seen := map[string][]string{}
	for _, s := range f.specs {
		id := "<nil>"
		if s.SessionID != nil {
			id = *s.SessionID
		}
		seen[s.CellID] = append(seen[s.CellID], id)
	}
	iso := seen["iso/auto"]
	if len(iso) != 3 || iso[0] == iso[1] || iso[1] == iso[2] || iso[0] == iso[2] {
		t.Errorf("isolate session ids must all differ: %v", iso)
	}
	st := seen["st/auto"]
	if len(st) != 3 || st[0] != st[1] || st[1] != st[2] || st[0] == "<nil>" {
		t.Errorf("sticky session ids must be one fixed value: %v", st)
	}
	for _, id := range seen["im/auto"] {
		if id != "<nil>" {
			t.Errorf("implicit trial sent session id %q", id)
		}
	}
}

func TestBudgetHaltsRun(t *testing.T) {
	// One sticky sequence so the order is deterministic: 0.01 per trial
	// against a 0.025 cap must stop after the third trial.
	f := &fake{reply: func(int, transport.TrialSpec) (transport.TrialResult, error) { return ok(0.01) }}
	h := newHarness(t, f, 0.025, 100)
	plan := BuildPlan(cells(t, "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, stickiness: sticky, prompt_id: p1, repeats: 10}\n"))
	sum, err := h.r.Run(context.Background(), plan)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if len(f.specs) != 3 || sum.Completed != 3 || math.Abs(sum.SpentUSD-0.03) > 1e-9 {
		t.Errorf("sent %d, completed %d, spent %v; want 3, 3, 0.03", len(f.specs), sum.Completed, sum.SpentUSD)
	}
	if n := len(h.records(t)); n != 3 {
		t.Errorf("%d records logged, want 3: the trial that breached the cap must still be recorded", n)
	}
}

// With trials running concurrently, a breach must stop the rest: the overshoot
// is bounded by what was already in flight.
func TestBudgetBoundsConcurrentOvershoot(t *testing.T) {
	f := &fake{delay: 20 * time.Millisecond, reply: func(int, transport.TrialSpec) (transport.TrialResult, error) { return ok(1) }}
	h := newHarness(t, f, 0.5, 100)
	plan := BuildPlan(cells(t, "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, prompt_id: p1, repeats: 40}\n"))
	_, err := h.r.Run(context.Background(), plan)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if n := len(f.specs); n > Concurrency {
		t.Errorf("%d trials completed after a first-trial breach, want at most %d", n, Concurrency)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	f := &fake{delay: 15 * time.Millisecond, reply: func(int, transport.TrialSpec) (transport.TrialResult, error) { return ok(0) }}
	h := newHarness(t, f, 1, 100)
	plan := BuildPlan(cells(t, "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, prompt_id: p1, repeats: 20}\n"))
	if _, err := h.r.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if p := f.peak.Load(); p != Concurrency {
		t.Errorf("peak concurrency = %d, want %d", p, Concurrency)
	}
}

func TestTrialCapRefusesWholePlan(t *testing.T) {
	f := &fake{reply: func(int, transport.TrialSpec) (transport.TrialResult, error) { return ok(0) }}
	h := newHarness(t, f, 1, 4)
	_, err := h.r.Run(context.Background(), BuildPlan(cells(t, isolate5)))
	if !errors.Is(err, ErrTooManyTrials) || len(f.specs) != 0 {
		t.Fatalf("err = %v with %d requests sent; want ErrTooManyTrials and none", err, len(f.specs))
	}
}

func TestRetryPolicy(t *testing.T) {
	one := "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, prompt_id: p1}\n"
	tests := []struct {
		name         string
		reply        func(n int) (transport.TrialResult, error)
		wantAttempts int
		wantFailed   int
		wantSleeps   []time.Duration // nil means "only check the count"
		wantSleepN   int
		wantErr      error
	}{
		{"success first time", func(int) (transport.TrialResult, error) { return ok(0) }, 1, 0, nil, 0, nil},
		{"404 is never retried", func(int) (transport.TrialResult, error) { return status(404, nil) }, 1, 1, nil, 0, nil},
		{"400 is never retried", func(int) (transport.TrialResult, error) { return status(400, nil) }, 1, 1, nil, 0, nil},
		{"402 is never retried", func(int) (transport.TrialResult, error) { return status(402, nil) }, 1, 1, nil, 0, nil},
		{"500 retried to the limit", func(int) (transport.TrialResult, error) { return status(500, nil) }, 3, 1, nil, 2, nil},
		{"429 honours Retry-After", func(n int) (transport.TrialResult, error) {
			if n == 1 {
				return status(429, http.Header{"Retry-After": {"7"}})
			}
			return ok(0)
		}, 2, 0, []time.Duration{7 * time.Second}, 1, nil},
		{"huge Retry-After is capped", func(n int) (transport.TrialResult, error) {
			if n == 1 {
				return status(503, http.Header{"Retry-After": {"86400"}})
			}
			return ok(0)
		}, 2, 0, []time.Duration{maxRetryAfter}, 1, nil},
		{"no response is retried", func(n int) (transport.TrialResult, error) {
			if n < 3 {
				return transport.TrialResult{}, transport.ErrNoResponse
			}
			return ok(0)
		}, 3, 0, nil, 2, nil},
		{"blocked request stops the run", func(int) (transport.TrialResult, error) {
			return transport.TrialResult{}, transport.ErrPluginMismatch
		}, 1, 0, nil, 0, transport.ErrPluginMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{reply: func(n int, _ transport.TrialSpec) (transport.TrialResult, error) { return tc.reply(n) }}
			h := newHarness(t, f, 100, 100)
			sum, err := h.r.Run(context.Background(), BuildPlan(cells(t, one)))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(f.specs) != tc.wantAttempts || sum.Failed != tc.wantFailed || len(h.slept) != tc.wantSleepN {
				t.Errorf("attempts %d, failed %d, sleeps %v; want %d, %d, %d sleeps", len(f.specs), sum.Failed, h.slept, tc.wantAttempts, tc.wantFailed, tc.wantSleepN)
			}
			for i, w := range tc.wantSleeps {
				if h.slept[i] != w {
					t.Errorf("sleep %d = %v, want %v", i, h.slept[i], w)
				}
			}
			if recs := h.records(t); len(recs) != 1 {
				t.Errorf("%d records, want exactly 1 per trial", len(recs))
			}
		})
	}
}

// Backoff without Retry-After grows and stays within its bounds.
func TestBackoffBounds(t *testing.T) {
	r := &Runner{opts: Options{BackoffBase: time.Second, BackoffMax: 4 * time.Second}}
	for attempt, ceiling := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 4 * time.Second} {
		for range 200 {
			d := r.backoff(attempt, transport.TrialResult{})
			if d < ceiling/2 || d > ceiling {
				t.Fatalf("attempt %d: backoff %v outside [%v, %v]", attempt, d, ceiling/2, ceiling)
			}
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"5", 5 * time.Second, true},
		{"0", 0, true},
		{"-3", 0, false},
		{"soon", 0, false},
		{"Fri, 09 Oct 2026 12:00:30 GMT", 30 * time.Second, true},
		{"Fri, 09 Oct 2026 11:00:00 GMT", 0, true},
	}
	for _, tc := range tests {
		got, ok := retryAfter(tc.in, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A success that reports no cost, and a request that got no response, are
// charged at the estimate rather than treated as free.
func TestUnknownCostIsAssumedNotFree(t *testing.T) {
	one := "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, prompt_id: p1}\n"
	f := &fake{reply: func(n int, _ transport.TrialSpec) (transport.TrialResult, error) {
		if n == 1 {
			return transport.TrialResult{}, transport.ErrNoResponse
		}
		return transport.TrialResult{Status: 200, Exchange: &transport.Exchange{}}, nil
	}}
	h := newHarness(t, f, 100, 100)
	sum, err := h.r.Run(context.Background(), BuildPlan(cells(t, one)))
	if err != nil {
		t.Fatal(err)
	}
	if sum.SpentUSD != 1.0 {
		t.Errorf("spent = %v, want 1.0 (two attempts at the 0.5 estimate)", sum.SpentUSD)
	}
	rec := h.records(t)[0]
	if rec["cost_assumed"] != true || rec["cost_usd"] != 1.0 {
		t.Errorf("record cost_usd=%v cost_assumed=%v", rec["cost_usd"], rec["cost_assumed"])
	}
}

func TestRecordFields(t *testing.T) {
	f := &fake{reply: func(int, transport.TrialSpec) (transport.TrialResult, error) {
		tt := "math"
		c := 0.002
		return transport.TrialResult{Status: 200, Model: "v/m", PluginID: "auto-router", TaskType: &tt, CostUSD: &c, GenerationID: "gen-1", Exchange: &transport.Exchange{}}, nil
	}}
	h := newHarness(t, f, 1, 10)
	plan := BuildPlan(cells(t, "version: 1\nexperiments:\n  - {id: a, router: auto, transport: chat_completions, prompt_id: p1, plugin: {cost_quality_tradeoff: 0}}\n"))
	if _, err := h.r.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	rec := h.records(t)[0]
	for k, want := range map[string]any{
		"run_id": "run1", "experiment_id": "a", "cell_id": "a/auto", "router": "auto", "plugin_id": "auto-router",
		"model": "v/m", "task_type": "math", "status": 200.0, "cost_usd": 0.002, "generation_id": "gen-1",
		"prompt_id": "p1", "stickiness": "isolate", "transport": "chat_completions", "head_to_head": false,
	} {
		if rec[k] != want {
			t.Errorf("record[%q] = %v (%T), want %v", k, rec[k], rec[k], want)
		}
	}
	// The recorded config must keep the zero dial distinct from an unset one.
	pc, _ := json.Marshal(rec["plugin_config"])
	if !strings.Contains(string(pc), `"CostQualityTradeoff":0`) {
		t.Errorf("plugin_config lost the zero dial: %s", pc)
	}
}

func TestEstimate(t *testing.T) {
	var models []string
	for i := 1; i <= 10; i++ {
		// completion prices 1..10 USD per million tokens
		models = append(models, `{"pricing":{"prompt":"0","completion":"`+strings.Repeat("0", 0)+formatPerToken(i)+`"}}`)
	}
	body := `{"data":[` + strings.Join(models, ",") + `,{"pricing":{"prompt":"-1","completion":"-1"}},{"pricing":{"prompt":"x","completion":"y"}},{}]}`
	cat, err := ParseCatalog([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.completion) != 10 {
		t.Fatalf("priced models = %d, want 10 (routers and junk skipped)", len(cat.completion))
	}
	tier := func(s string) *config.Plugin { v := config.CostTier(s); return &config.Plugin{CostTier: &v} }
	dial := func(d int) *config.Plugin { return &config.Plugin{CostQualityTradeoff: &d} }
	tests := []struct {
		name   string
		plugin *config.Plugin
		want   float64 // USD per million completion tokens at the ceiling
	}{
		{"no setting", nil, 2},
		{"low", tier("low"), 2},
		{"medium", tier("medium"), 4},
		{"high", tier("high"), 6},
		{"xhigh", tier("xhigh"), 8},
		{"max", tier("max"), 10},
		{"dial 10", dial(10), 1},
		{"dial 0", dial(0), 9},
		{"allow-list", &config.Plugin{AllowedModels: []string{"x/*"}}, 10},
	}
	for _, tc := range tests {
		cell := config.Cell{MaxTokens: 1_000_000, Experiment: config.Experiment{Plugin: tc.plugin}}
		if got := cat.TrialCeiling(cell, config.Prompt{}); math.Abs(got-tc.want) > 1e-6 {
			t.Errorf("%s: ceiling = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A max_price cap bounds the estimate: max band, capped at 3 USD/M.
	capped := config.Cell{MaxTokens: 1_000_000, Experiment: config.Experiment{
		Plugin:   tier("max"),
		Provider: &config.Provider{MaxPrice: &config.MaxPrice{Completion: new(3.0)}},
	}}
	if got := cat.TrialCeiling(capped, config.Prompt{}); math.Abs(got-3) > 1e-6 {
		t.Errorf("capped ceiling = %v, want 3", got)
	}
	if _, err := ParseCatalog([]byte(`{"data":[]}`)); err == nil {
		t.Error("empty catalog accepted")
	}
}
