// orlab sends the same logical prompt through OpenRouter's three API
// dialects and reports, phase by phase, what each one actually does.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/analyze"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/config"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/dialect"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/openrouter"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/prompts"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/record"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/redact"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/runner"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/transport"
)

// defaultModels deliberately excludes Anthropic and OpenAI slugs: the point
// is dialect/model-family mismatch. Every slug is validated against
// GET /models at preflight.
var defaultModels = []string{
	"x-ai/grok-4.6",
	"google/gemini-2.5-flash",
	"deepseek/deepseek-chat",
	"mistralai/mistral-small-2603",
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "orlab:", redact.String(err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], config.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runID := time.Now().UTC().Format("20060102-150405")
	runDir := filepath.Join(cfg.OutDir, runID)
	rec, err := record.NewWriter(runDir, "run.jsonl")
	if err != nil {
		return err
	}
	defer rec.Close()

	logFile, err := os.OpenFile(filepath.Join(runDir, "log.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 G703 -- operator-supplied output path
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(fanout{
		slog.NewJSONHandler(logFile, nil),
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
	})

	transcript, err := os.OpenFile(filepath.Join(runDir, "transcript.txt"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 G703 -- operator-supplied output path
	if err != nil {
		return err
	}
	defer transcript.Close()

	suite, err := prompts.Load(cfg.SuitePath)
	if err != nil {
		return err
	}
	models := cfg.Models
	if len(models) == 0 {
		models = defaultModels
	}

	var doer runner.Doer
	reconcileDelays := []time.Duration(nil)
	replay := cfg.ReplayDir != ""
	if replay {
		rp, err := record.LoadReplayer(cfg.ReplayDir)
		if err != nil {
			return err
		}
		log.Info("replay mode", "dir", cfg.ReplayDir, "exchanges", rp.Len())
		doer = rp
		reconcileDelays = []time.Duration{time.Millisecond}
	} else {
		tc, err := transport.New(transport.Options{
			BaseURL: cfg.BaseURL,
			OnRetry: func(ev transport.RetryEvent) {
				log.Warn("retry", "attempt", ev.Attempt, "status", ev.Status,
					"wait", ev.Wait.Round(time.Millisecond), "retry_after_header", ev.RetryAfter)
			},
		})
		if err != nil {
			return err
		}
		// Record every exchange (completions, /models, /generation) so the
		// run is replayable offline.
		doer = &record.RecordingDoer{Inner: tc, W: rec}
	}

	log.Info("orlab starting",
		"run_id", runID,
		"key", redact.Fingerprint(cfg.APIKey),
		"base_url", cfg.BaseURL,
		"models", models,
		"entries", len(suite),
		"stream", cfg.Stream,
		"echo_upstream", cfg.EchoUpstream,
		"dry_run", cfg.DryRun,
		"replay", replay,
	)

	r := &runner.Runner{
		Cfg: cfg,
		Do:  doer,
		OR: &openrouter.Client{
			Do: doer, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey,
			ReconcileDelays: reconcileDelays,
		},
		Dialects:   []dialect.Dialect{dialect.Chat{}, dialect.Responses{}, dialect.Messages{}},
		Rec:        rec,
		Log:        log,
		Transcript: transcript,
		Replay:     replay,
	}
	data, err := r.Run(ctx, suite, models)
	if err != nil {
		return err
	}

	// Reports live alongside the run's records.
	transcriptPath := filepath.Join(runDir, "transcript.txt")
	reportsDir := filepath.Join(runDir, "reports")
	if err := analyze.WriteReports(reportsDir, data, transcriptPath); err != nil {
		return err
	}

	log.Info("run complete",
		"run_id", runID,
		"outcomes", len(data.Outcomes),
		"partial", data.Partial,
		"dropped_records", rec.Dropped(),
		"records", filepath.Join(runDir, "run.jsonl"),
		"analysis", filepath.Join(reportsDir, "ANALYSIS.md"),
	)
	if rec.Dropped() > 0 {
		log.Warn("some records were dropped by fail-closed redaction", "count", rec.Dropped())
	}
	return nil
}

// fanout sends every log record to all handlers (JSON to file, text to
// stderr).
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
