// Command backfill enriches logged trials with per-generation statistics from
// GET /api/v1/generation. It reads the trial log, writes a separate JSONL
// file, and can be re-run at any time: ids already fetched are skipped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/backfill"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/logging"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/transport"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatalf("backfill: %v", err)
	}
}

func run() (err error) {
	var (
		logPath = flag.String("log", "logs/trials.jsonl", "JSONL trial log to read")
		outPath = flag.String("out", "logs/generations.jsonl", "append-only JSONL file for generation stats")
		runID   = flag.String("run", "", "only backfill this run id (default: every run in the log)")
		limit   = flag.Int("max", 1000, "refuse to look up more generations than this in one go")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	key, err := config.APIKey()
	if err != nil {
		return err
	}
	// The output holds run data, so it must be ignored by git.
	if err := logging.EnsureIgnored(*outPath); err != nil {
		return err
	}

	done := map[string]bool{}
	if existing, oerr := os.Open(*outPath); oerr == nil { // #nosec G304 -- operator's flag
		done, err = backfill.Done(existing)
		if cerr := existing.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	} else if !errors.Is(oerr, fs.ErrNotExist) {
		return oerr
	}

	trials, err := os.Open(*logPath) // #nosec G304 -- operator's flag
	if err != nil {
		return err
	}
	targets, err := backfill.Targets(trials, *runID, done)
	if cerr := trials.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if len(targets) > *limit {
		return fmt.Errorf("%d generations to look up, limit is %d; raise -max or pass -run", len(targets), *limit)
	}
	fmt.Printf("%d generations to look up, %d already present\n", len(targets), len(done))
	if len(targets) == 0 {
		return nil
	}

	out, err := logging.OpenLog(*outPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", *outPath, cerr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sum, err := backfill.Run(ctx, targets, backfill.Options{
		Client: transport.NewHTTPClient(false, transport.DefaultGuards()),
		Out:    logging.NewJSON(out, logging.Options{Secrets: []string{key}}),
	})
	fmt.Printf("fetched %d, not available yet %d, failed %d; wrote %s\n", sum.Fetched, sum.Missing, sum.Failed, *outPath)
	return err
}
