// Command lab runs the Auto Router experiment matrix.
//
//	lab -dry-run                 render every trial's payload, send nothing
//	lab -max-spend 0.25          run the matrix under a spend cap
//	lab -max-spend 0.05 -only smoke-low,tier-sweep-low
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/logging"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/runner"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/transport"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatalf("lab: %v", err)
	}
}

func run() (err error) {
	var (
		experiments  = flag.String("config", "experiments.yaml", "experiment matrix YAML")
		prompts      = flag.String("prompts", "prompts.yaml", "prompt library YAML")
		logPath      = flag.String("log", "logs/trials.jsonl", "append-only JSONL trial log")
		dryRun       = flag.Bool("dry-run", false, "render and log every trial's payload without sending anything")
		only         = flag.String("only", "", "comma-separated experiment ids to run (default: all)")
		maxSpend     = flag.Float64("max-spend", 0, "halt the run once spend exceeds this many USD (required for a live run)")
		maxTrials    = flag.Int("max-trials", 300, "refuse a plan with more trials than this")
		confirmAbove = flag.Float64("confirm-above", 0.10, "ask for confirmation when the estimated cost in USD exceeds this")
		logBodies    = flag.Bool("log-bodies", false, "record prompt and response text in the logs")
		keyLast4     = flag.Bool("debug-key-fingerprint", false, "log the last 4 characters of the API key")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	if !*dryRun && *maxSpend <= 0 {
		return errors.New("a live run needs -max-spend <USD>; use -dry-run to render without sending")
	}

	key, err := config.APIKey()
	if err != nil {
		return err
	}
	cells, err := config.Load(*experiments, *prompts)
	if err != nil {
		return err
	}
	if cells, err = selectCells(cells, *only); err != nil {
		return err
	}
	plan := runner.BuildPlan(cells)

	// The log may hold prompts and model output, so it must be ignored by git
	// before the first byte is written.
	if err := logging.EnsureIgnored(*logPath); err != nil {
		return err
	}
	f, err := logging.OpenLog(*logPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close log: %w", cerr)
		}
	}()

	opts := logging.Options{LogBodies: *logBodies, Secrets: []string{key}}
	console := slog.New(logging.NewTable(os.Stdout, opts, []logging.Column{
		{Key: "cell", Width: 30},
		{Key: "rep", Width: 4},
		{Key: "status", Width: 6},
		{Key: "model", Width: 34},
		{Key: "task", Width: 26},
		{Key: "cost", Width: 9},
		{Key: "ms", Width: 6},
		{Key: "note", Width: 60},
	}))
	if *keyLast4 {
		console.Info("api key", "key_last4", logging.KeyLast4(key))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	hc := transport.NewHTTPClient(*dryRun, transport.DefaultGuards())
	estimate := func(runner.Trial) float64 { return 0 }
	spendCap := *maxSpend
	if *dryRun {
		spendCap = 1 // nothing is sent, so nothing is spent; the runner still wants a cap
	} else {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cat, cerr := runner.FetchCatalog(cctx, hc, runner.CatalogURL)
		cancel()
		if cerr != nil {
			return fmt.Errorf("cannot estimate cost, so not running: %w", cerr)
		}
		estimate = func(t runner.Trial) float64 { return cat.TrialCeiling(t.Cell, t.Prompt) }
		if err := confirm(plan, estimate, *maxSpend, *confirmAbove); err != nil {
			return err
		}
	}

	runID, err := runner.NewID()
	if err != nil {
		return err
	}
	messages, err := transport.NewMessages(hc, transport.DefaultBaseURL, key)
	if err != nil {
		return err
	}
	r, err := runner.New(
		map[config.TransportKind]transport.Transport{
			config.TransportChatCompletions: transport.NewChat(hc, ""),
			config.TransportMessages:        messages,
		},
		runner.Options{
			RunID:       runID,
			MaxSpendUSD: spendCap,
			MaxTrials:   *maxTrials,
			Estimate:    estimate,
			MaxAttempts: 3,
			BackoffBase: time.Second,
			BackoffMax:  30 * time.Second,
			Trials:      logging.NewJSON(f, opts),
			Console:     console,
		})
	if err != nil {
		return err
	}
	sum, runErr := r.Run(ctx, plan)
	console.Info("run finished", "run_id", runID, "planned", sum.Planned, "completed", sum.Completed,
		"non_2xx", sum.Failed, "assert_failed", sum.AssertFailed, "spent_usd", fmt.Sprintf("%.6f", sum.SpentUSD), "log", *logPath)
	return runErr
}

// selectCells keeps the cells of the named experiments. An id that matches
// nothing is an error, so a typo cannot quietly shrink the run.
func selectCells(cells []config.Cell, only string) ([]config.Cell, error) {
	if only == "" {
		return cells, nil
	}
	want := strings.Split(only, ",")
	var out []config.Cell
	for _, c := range cells {
		if slices.Contains(want, c.Experiment.ID) {
			out = append(out, c)
		}
	}
	for _, id := range want {
		if !slices.ContainsFunc(cells, func(c config.Cell) bool { return c.Experiment.ID == id }) {
			return nil, fmt.Errorf("-only: no experiment with id %q", id)
		}
	}
	return out, nil
}

// confirm prints the cost estimate and, above the threshold, waits for the
// operator to type yes.
func confirm(plan runner.Plan, estimate func(runner.Trial) float64, maxSpend, threshold float64) error {
	type line struct {
		trials int
		usd    float64
	}
	byCell := map[string]*line{}
	var order []string
	total := 0.0
	for _, seq := range plan.Sequences {
		for _, t := range seq {
			l := byCell[t.Cell.ID]
			if l == nil {
				l = &line{}
				byCell[t.Cell.ID] = l
				order = append(order, t.Cell.ID)
			}
			e := estimate(t)
			l.trials++
			l.usd += e
			total += e
		}
	}
	slices.Sort(order)
	fmt.Printf("Estimated ceiling per cell (list-price percentile, not a guarantee):\n")
	for _, id := range order {
		fmt.Printf("  %-34s %3d trials  $%.6f\n", id, byCell[id].trials, byCell[id].usd)
	}
	fmt.Printf("Total: %d trials, estimated up to $%.4f; spend cap $%.4f\n", plan.Trials, total, maxSpend)
	if total > maxSpend {
		fmt.Printf("Note: the estimate exceeds the spend cap, so the run may halt before it finishes.\n")
	}
	if total <= threshold {
		return nil
	}
	fmt.Printf("Estimate is above $%.2f. Type yes to continue: ", threshold)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "yes" {
		return errors.New("not confirmed; nothing was sent")
	}
	return nil
}
