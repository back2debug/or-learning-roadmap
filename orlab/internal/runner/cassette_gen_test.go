package runner

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/prompts"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/record"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/transport"
)

var updateCassettes = flag.Bool("update-cassettes", false,
	"regenerate the committed replay cassette from the fake OpenRouter server")

// TestGenerateSampleCassette records the embedded suite against the fake
// server into testdata/cassettes/, so `orlab -replay` is runnable by anyone
// reviewing the repo without a key or any spend. The data is synthetic: it
// comes from the httptest fake, not from OpenRouter.
func TestGenerateSampleCassette(t *testing.T) {
	if !*updateCassettes {
		t.Skip("pass -update-cassettes to regenerate")
	}
	dir := filepath.Join("..", "..", "testdata", "cassettes")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	srv := fakeOpenRouter(t)
	base := srv.URL + "/api/v1"
	tc, err := transport.New(transport.Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.NewWriter(dir, "run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	doer := &record.RecordingDoer{Inner: tc, W: rec}

	suite, err := prompts.Load("")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newTestRunner(t, base, filepath.Join(t.TempDir(), "meta"), doer, false)
	if _, err := r.Run(context.Background(), suite, []string{"test/model"}); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if rec.Dropped() > 0 {
		t.Fatalf("%d records dropped by redaction; cassette is incomplete", rec.Dropped())
	}
	t.Logf("wrote cassette to %s", dir)
}
