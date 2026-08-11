package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Replays the recorded results of the most recent run through Summarize and
// checks the block reaches learning_log.txt, not just the console.
func TestSummarizeReplay(t *testing.T) {
	raw, err := os.ReadFile("results.json")
	if err != nil {
		t.Skip("no results.json to replay")
	}
	var all []Result
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("unmarshal results.json: %v", err)
	}

	last := all[len(all)-1].Timestamp.Format("2006-01-02")
	var run []Result
	for _, r := range all {
		if r.Timestamp.Format("2006-01-02") == last {
			run = append(run, r)
		}
	}
	t.Logf("replaying %d results from %s", len(run), last)

	dir := t.TempDir()
	l, err := newLoggers(dir, "sk-or-fake")
	if err != nil {
		t.Fatalf("newLoggers: %v", err)
	}
	l.results = run
	l.Summarize()
	l.Close()

	logged, err := os.ReadFile(filepath.Join(dir, "learning_log.txt"))
	if err != nil {
		t.Fatalf("read learning_log.txt: %v", err)
	}
	got := string(logged)
	if !strings.Contains(got, "=== Findings summary") {
		t.Fatal("findings summary missing from learning_log.txt")
	}
	for _, want := range []string{"Tier routing", "Failures:", "Truncation:", "Cost by tier", "Latency by tier", "Takeaway:"} {
		if !strings.Contains(got, want) {
			t.Errorf("learning_log.txt missing section %q", want)
		}
	}
	t.Logf("learning_log.txt contents:\n%s", got)
}
