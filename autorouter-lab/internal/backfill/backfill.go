// Package backfill enriches logged trials with OpenRouter's per-generation
// statistics: native token counts and the cost as finally billed.
//
// It is deliberately separate from the runner. Enrichment happens after the
// fact, into its own file, so a backfill failure can never cost a trial and
// enrichment can be re-run without re-running any experiment.
package backfill

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/retry"
	"golang.org/x/sync/errgroup"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/transport"
)

const (
	concurrency = 3
	maxAttempts = 3
	perRequest  = 30 * time.Second
)

// Target is one generation to look up.
type Target struct {
	GenerationID string
	RunID        string
	TrialID      string
	CellID       string
}

// Targets reads a trial log and returns every successful live trial that has
// a generation id, skipping ids in done and any id seen twice. Lines that are
// not trial records are ignored.
func Targets(trials io.Reader, runID string, done map[string]bool) ([]Target, error) {
	sc := bufio.NewScanner(trials)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	seen := map[string]bool{}
	var out []Target
	for sc.Scan() {
		var rec struct {
			Msg          string `json:"msg"`
			RunID        string `json:"run_id"`
			TrialID      string `json:"trial_id"`
			CellID       string `json:"cell_id"`
			GenerationID string `json:"generation_id"`
			Status       int    `json:"status"`
			DryRun       bool   `json:"dry_run"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Msg != "trial" || rec.DryRun {
			continue
		}
		if rec.GenerationID == "" || rec.Status < 200 || rec.Status >= 300 {
			continue
		}
		if runID != "" && rec.RunID != runID {
			continue
		}
		if done[rec.GenerationID] || seen[rec.GenerationID] {
			continue
		}
		seen[rec.GenerationID] = true
		out = append(out, Target{GenerationID: rec.GenerationID, RunID: rec.RunID, TrialID: rec.TrialID, CellID: rec.CellID})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read trial log: %w", err)
	}
	return out, nil
}

// Done reads an existing backfill file and returns the generation ids that
// were fetched successfully, so a re-run only asks for what is missing.
func Done(existing io.Reader) (map[string]bool, error) {
	done := map[string]bool{}
	sc := bufio.NewScanner(existing)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var rec struct {
			Msg          string `json:"msg"`
			GenerationID string `json:"generation_id"`
			Status       int    `json:"status"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Msg == "generation" && rec.Status == http.StatusOK {
			done[rec.GenerationID] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read backfill file: %w", err)
	}
	return done, nil
}

// Options configures a backfill.
type Options struct {
	// BaseURL is empty in normal use and an httptest server in tests.
	BaseURL string
	Client  *http.Client
	// Out receives one record per generation looked up.
	Out *slog.Logger
	// Sleep waits between attempts. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Summary counts the outcomes of a backfill.
type Summary struct {
	Fetched int
	Missing int
	Failed  int
}

// Run looks up every target and writes one record each. A lookup that fails
// is recorded and counted, never fatal: the only error returned is the
// context ending.
func Run(ctx context.Context, targets []Target, o Options) (Summary, error) {
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	// As in the runner: no key passed (the SDK reads the environment) and the
	// SDK's own retry switched off so attempts are bounded here.
	opts := []openrouter.SDKOption{
		openrouter.WithClient(o.Client),
		openrouter.WithRetryConfig(retry.Config{Strategy: "none"}),
	}
	if o.BaseURL != "" {
		opts = append(opts, openrouter.WithServerURL(o.BaseURL))
	}
	sdk := openrouter.New(opts...)

	var (
		mu  sync.Mutex
		sum Summary
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for _, t := range targets {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			status, data, err := fetch(gctx, sdk, t.GenerationID, o.Sleep)
			if gctx.Err() != nil {
				return gctx.Err()
			}
			attrs := []any{
				"generation_id", t.GenerationID, "run_id", t.RunID, "trial_id", t.TrialID,
				"cell_id", t.CellID, "status", status, "generation", data,
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				sum.Failed++
				o.Out.Error("generation", append(attrs, "error", err)...)
			case status == http.StatusOK:
				sum.Fetched++
				o.Out.Info("generation", attrs...)
			case status == http.StatusNotFound:
				// Generation stats can lag the request; a later run retries.
				sum.Missing++
				o.Out.Warn("generation", attrs...)
			default:
				sum.Failed++
				o.Out.Warn("generation", attrs...)
			}
			return nil
		})
	}
	err := g.Wait()
	return sum, err
}

// fetch gets one generation, retrying 429 and 5xx. It returns the raw "data"
// object from the captured bytes, since the response schema is the API's to
// change and unknown fields should survive.
func fetch(ctx context.Context, sdk *openrouter.OpenRouter, id string, sleep func(context.Context, time.Duration) error) (int, json.RawMessage, error) {
	for attempt := 1; ; attempt++ {
		rctx, cancel := context.WithTimeout(ctx, perRequest)
		rctx, ex := transport.WithExchange(rctx)
		_, sdkErr := sdk.Generations.GetGeneration(rctx, id)
		cancel()

		if ex.Blocked != nil {
			return 0, nil, ex.Blocked
		}
		retryable := ex.Status == 0 || ex.Status == http.StatusTooManyRequests || ex.Status >= 500
		if !retryable || attempt >= maxAttempts {
			if ex.Status == 0 {
				return 0, nil, fmt.Errorf("no response: %w", errors.Join(transport.ErrNoResponse, sdkErr))
			}
			var doc struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(ex.ResponseBody, &doc) != nil {
				return ex.Status, nil, nil
			}
			return ex.Status, doc.Data, nil
		}
		if err := sleep(ctx, time.Duration(attempt)*time.Second); err != nil {
			return 0, nil, err
		}
	}
}
