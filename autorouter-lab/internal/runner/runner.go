// Package runner executes a plan of trials under a spend cap, with bounded
// concurrency and bounded retries, and writes one record per trial.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/assert"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/logging"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/transport"
)

var (
	// ErrBudgetExceeded stops a run once recorded spend passes the cap.
	ErrBudgetExceeded = errors.New("spend cap exceeded")
	// ErrTooManyTrials refuses a plan larger than the trial cap. The plan is
	// refused whole rather than truncated, so a run never quietly covers
	// less of the matrix than the config describes.
	ErrTooManyTrials = errors.New("plan exceeds the trial cap")
)

// Concurrency is how many sequences run at once.
const Concurrency = 3

// maxRetryAfter bounds how long a server-supplied Retry-After is honoured.
const maxRetryAfter = 60 * time.Second

// Trial is one request to send.
type Trial struct {
	Cell   config.Cell
	Repeat int
	// Step is the position within the cell's prompt sequence.
	Step   int
	Prompt config.Prompt
}

// Plan is the trials of a run, grouped into sequences. Sequences run
// concurrently; the trials inside one run strictly in order.
type Plan struct {
	Sequences [][]Trial
	Trials    int
}

// BuildPlan expands cells into trials.
//
// The grouping exists because of stickiness. An isolate trial has its own
// session and depends on nothing, so each is its own sequence. Sticky trials
// that share a session must run in a known order, or the experiment cannot say
// which request established the preference; implicit trials are identified by
// the server from message content, so they are all kept in one sequence.
func BuildPlan(cells []config.Cell) Plan {
	var plan Plan
	grouped := map[string]int{}
	for _, c := range cells {
		for rep := range c.Repeats {
			for step, p := range c.Prompts {
				t := Trial{Cell: c, Repeat: rep, Step: step, Prompt: p}
				plan.Trials++
				key := ""
				switch c.Experiment.Stickiness {
				case config.StickinessSticky:
					key = "cell:" + c.ID
					if c.Experiment.SessionID != nil {
						key = "session:" + *c.Experiment.SessionID
					}
				case config.StickinessImplicit:
					key = "implicit"
				}
				if key == "" {
					plan.Sequences = append(plan.Sequences, []Trial{t})
					continue
				}
				i, ok := grouped[key]
				if !ok {
					i = len(plan.Sequences)
					grouped[key] = i
					plan.Sequences = append(plan.Sequences, nil)
				}
				plan.Sequences[i] = append(plan.Sequences[i], t)
			}
		}
	}
	return plan
}

// Options configures a run.
type Options struct {
	RunID string
	// MaxSpendUSD halts the run once recorded spend exceeds it. Trials
	// already in flight when the cap is hit are cancelled, so the overshoot
	// is bounded by Concurrency trials.
	MaxSpendUSD float64
	MaxTrials   int
	// Estimate is the assumed cost of an attempt whose real cost is unknown:
	// a success that reported no cost, or a request that got no response and
	// may still have been billed. Unknown never counts as free.
	Estimate func(Trial) float64
	// MaxAttempts bounds tries per trial, including the first.
	MaxAttempts int
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// Sleep waits for d or until ctx ends. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Trials is the JSONL sink, Console the live table.
	Trials  *slog.Logger
	Console *slog.Logger
}

// Summary describes a finished or halted run.
type Summary struct {
	Planned   int
	Completed int
	// Failed counts trials whose final outcome was not a 2xx. Some of those
	// are meant to fail; AssertFailed is the number that matters.
	Failed int
	// AssertFailed counts trials with at least one failed assertion: the
	// response contradicted the config that was sent.
	AssertFailed int
	SpentUSD     float64
}

// Runner executes plans.
type Runner struct {
	transports map[config.TransportKind]transport.Transport
	opts       Options

	mu      sync.Mutex
	summary Summary
}

// New builds a runner. transports maps each API surface to its transport.
func New(transports map[config.TransportKind]transport.Transport, opts Options) (*Runner, error) {
	switch {
	case opts.MaxSpendUSD <= 0:
		return nil, errors.New("runner: a positive spend cap is required")
	case opts.MaxTrials <= 0:
		return nil, errors.New("runner: a positive trial cap is required")
	case opts.MaxAttempts <= 0:
		return nil, errors.New("runner: max attempts must be positive")
	case opts.Estimate == nil || opts.Trials == nil || opts.Console == nil:
		return nil, errors.New("runner: estimate, trial log and console are required")
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	return &Runner{transports: transports, opts: opts}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

// Run executes the plan. It returns the summary in every case; the error is
// non-nil when the run stopped early.
func (r *Runner) Run(ctx context.Context, plan Plan) (Summary, error) {
	r.summary = Summary{Planned: plan.Trials}
	if plan.Trials > r.opts.MaxTrials {
		return r.summary, fmt.Errorf("%w: %d trials planned, cap is %d", ErrTooManyTrials, plan.Trials, r.opts.MaxTrials)
	}
	for _, seq := range plan.Sequences {
		for _, t := range seq {
			if r.transports[t.Cell.Experiment.Transport] == nil {
				return r.summary, fmt.Errorf("cell %s: transport %q is not available", t.Cell.ID, t.Cell.Experiment.Transport)
			}
		}
	}

	// One shared context: the first sequence to return an error (a budget
	// breach, a blocked request) cancels every trial still in flight.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(Concurrency)
	for _, seq := range plan.Sequences {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			for _, t := range seq {
				if err := r.trial(gctx, t); err != nil {
					return err
				}
			}
			return nil
		})
	}
	err := g.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summary, err
}

// trial runs one trial to its final outcome. It returns an error only when the
// whole run must stop.
func (r *Runner) trial(ctx context.Context, t Trial) error {
	if err := r.checkBudget(); err != nil {
		return err
	}
	if w := t.Cell.Experiment.WaitBeforeSeconds; w != nil && *w > 0 && t.Repeat == 0 && t.Step == 0 {
		if err := r.opts.Sleep(ctx, time.Duration(*w)*time.Second); err != nil {
			return err
		}
	}
	spec, err := r.spec(t)
	if err != nil {
		return err
	}
	tr := r.transports[t.Cell.Experiment.Transport]

	var (
		res      transport.TrialResult
		retries  []retry
		cost     float64
		assumed  bool
		finalErr error
	)
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		res, err = tr.Run(ctx, spec)
		if errors.Is(err, transport.ErrDryRun) {
			r.record(t, spec, res, nil, 0, false, nil)
			return nil
		}
		if err != nil && !errors.Is(err, transport.ErrNoResponse) {
			// The trial could not be sent as specified. Sending something
			// else instead is not an option, so the run stops.
			r.record(t, spec, res, retries, cost, assumed, err)
			return fmt.Errorf("cell %s: %w", t.Cell.ID, err)
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}

		// Account for the attempt before deciding anything else.
		switch {
		case res.CostUSD != nil:
			cost += *res.CostUSD
			r.spend(*res.CostUSD)
		case err != nil || (res.Status >= 200 && res.Status < 300):
			est := r.opts.Estimate(t)
			cost += est
			assumed = true
			r.spend(est)
		}

		retryable := err != nil || res.Status == http.StatusTooManyRequests || res.Status >= 500
		if !retryable || attempt >= r.opts.MaxAttempts {
			finalErr = err
			break
		}
		if berr := r.checkBudget(); berr != nil {
			r.record(t, spec, res, retries, cost, assumed, err)
			return berr
		}
		wait := r.backoff(attempt, res)
		retries = append(retries, retry{Status: res.Status, WaitMS: wait.Milliseconds()})
		if serr := r.opts.Sleep(ctx, wait); serr != nil {
			return serr
		}
	}

	failed := r.record(t, spec, res, retries, cost, assumed, finalErr)
	r.mu.Lock()
	r.summary.Completed++
	if failed {
		r.summary.AssertFailed++
	}
	if finalErr != nil || res.Status < 200 || res.Status >= 300 {
		r.summary.Failed++
	}
	r.mu.Unlock()
	return r.checkBudget()
}

func (r *Runner) spend(usd float64) {
	r.mu.Lock()
	r.summary.SpentUSD += usd
	r.mu.Unlock()
}

func (r *Runner) checkBudget() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summary.SpentUSD > r.opts.MaxSpendUSD {
		return fmt.Errorf("%w: spent $%.6f of $%.6f", ErrBudgetExceeded, r.summary.SpentUSD, r.opts.MaxSpendUSD)
	}
	return nil
}

// backoff picks the wait before the next attempt: the server's Retry-After
// when it sent one, otherwise exponential backoff with jitter so concurrent
// trials do not retry in step.
func (r *Runner) backoff(attempt int, res transport.TrialResult) time.Duration {
	if res.Exchange != nil {
		if d, ok := retryAfter(res.Exchange.ResponseHeader.Get("Retry-After"), time.Now()); ok {
			return min(d, maxRetryAfter)
		}
	}
	ceiling := min(r.opts.BackoffBase<<(attempt-1), r.opts.BackoffMax)
	if ceiling <= 0 {
		return 0
	}
	half := ceiling / 2
	return half + mrand.N(half+1) // #nosec G404 -- jitter, not a secret
}

// retryAfter parses a Retry-After header in either of its two forms.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// spec renders a trial into what the transport sends. The session id is where
// stickiness modes differ; it always travels in the request body.
func (r *Runner) spec(t Trial) (transport.TrialSpec, error) {
	id, err := NewID()
	if err != nil {
		return transport.TrialSpec{}, err
	}
	e := t.Cell.Experiment
	spec := transport.TrialSpec{
		TrialID:   id,
		CellID:    t.Cell.ID,
		Repeat:    t.Repeat,
		Router:    t.Cell.Target.Router,
		Prompt:    t.Prompt.Text,
		MaxTokens: t.Cell.MaxTokens,
		Plugin:    e.Plugin,
		Provider:  e.Provider,
		Timeout:   time.Duration(t.Cell.Timeout) * time.Second,
	}
	switch e.Stickiness {
	case config.StickinessIsolate:
		spec.SessionID = new("arl-" + id)
	case config.StickinessSticky:
		sid := "arl-" + r.opts.RunID + "-" + e.ID
		if e.SessionID != nil {
			// The run id keeps a fixed session from colliding with the
			// same experiment's previous run, which may still be remembered.
			sid = "arl-" + r.opts.RunID + "-" + *e.SessionID
		}
		spec.SessionID = &sid
	case config.StickinessImplicit:
	}
	return spec, nil
}

type retry struct {
	Status int   `json:"status"`
	WaitMS int64 `json:"wait_ms"`
}

// record writes the trial to the JSONL sink and the console, and reports
// whether any assertion failed.
func (r *Runner) record(t Trial, spec transport.TrialSpec, res transport.TrialResult, retries []retry, cost float64, assumed bool, err error) bool {
	e := t.Cell.Experiment
	expect := 0
	if e.ExpectStatus != nil {
		expect = *e.ExpectStatus
	}
	attrs := []any{
		"run_id", r.opts.RunID,
		"trial_id", spec.TrialID,
		"experiment_id", e.ID,
		"cell_id", t.Cell.ID,
		"repeat", t.Repeat,
		"step", t.Step,
		"head_to_head", t.Cell.HeadToHead,
		"transport", e.Transport,
		"router", t.Cell.Target.Router,
		"model_slug", t.Cell.Target.ModelSlug,
		"plugin_id", res.PluginID,
		"stickiness", e.Stickiness,
		"session_id", spec.SessionID,
		"prompt_id", t.Prompt.ID,
		"expected_task_type", t.Prompt.ExpectedTaskType,
		"expect_status", expect,
		"plugin_config", e.Plugin,
		"provider_config", e.Provider,
		"status", res.Status,
		"model", res.Model,
		"resolved_to", res.ResolvedTo,
		"model_fallback", res.ModelFallback,
		"upstream_attempts", res.UpstreamAttempts,
		"provider", res.Provider,
		"task_type", res.TaskType,
		"generation_id", res.GenerationID,
		"body_id", res.BodyID,
		"usage", res.Usage,
		"cost_usd", cost,
		"cost_assumed", assumed,
		"latency_ms", res.Latency.Milliseconds(),
		"retries", retries,
		"openrouter_metadata", res.Metadata,
		"api_error", res.APIError,
		"sdk_error", res.SDKError,
	}
	dry := false
	if res.Exchange != nil {
		dry = res.Exchange.DryRun
		attrs = append(attrs, "dry_run", dry, "request_payload", logging.Payload(res.Exchange.RequestBody), "wire", res.Exchange)
	}
	// The checks read the same fields the record carries, and the request
	// exactly as it was sent.
	var failures []string
	if !dry && res.Exchange != nil && res.Status != 0 {
		results := assert.CheckTrial(assert.Trial{
			CellID: t.Cell.ID, Router: string(t.Cell.Target.Router), Transport: string(e.Transport),
			Stickiness: string(e.Stickiness), ExpectStatus: expect, Plugin: e.Plugin,
			Status: res.Status, Model: res.Model, ResolvedTo: res.ResolvedTo, ModelFallback: res.ModelFallback,
			TaskType: res.TaskType, Payload: res.Exchange.RequestBody,
		})
		attrs = append(attrs, "assertions", results)
		for _, a := range results {
			if a.Status == assert.Fail {
				failures = append(failures, a.Check)
			}
		}
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelError
		attrs = append(attrs, "error", err)
	}
	r.opts.Trials.Log(context.Background(), level, "trial", attrs...)

	note := ""
	switch {
	case dry:
		note = "dry run"
	case err != nil:
		note = err.Error()
	case len(failures) > 0:
		note = "ASSERT FAIL: " + strings.Join(failures, ", ")
	case len(res.APIError) > 0:
		note = string(res.APIError)
	case len(retries) > 0:
		note = fmt.Sprintf("%d retries", len(retries))
	case res.ModelFallback:
		note = "fallback model; router chose " + res.ResolvedTo
	}
	task := ""
	if res.TaskType != nil {
		task = *res.TaskType
	}
	r.opts.Console.Info("trial",
		"cell", t.Cell.ID, "rep", fmt.Sprintf("%d.%d", t.Repeat, t.Step), "status", res.Status,
		"model", res.Model, "task", task, "cost", fmt.Sprintf("%.6f", cost),
		"ms", res.Latency.Milliseconds(), "note", note)
	return len(failures) > 0
}

// NewID returns a random identifier for runs and trials.
func NewID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
