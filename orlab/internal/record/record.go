// Package record persists run data as redacted JSONL and replays recorded
// HTTP exchanges. Redaction is fail-closed: a line that still smells of a
// credential after scrubbing is dropped, never written.
package record

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/redact"
)

// Line is the envelope for every JSONL record.
type Line struct {
	Type string          `json:"type"` // "meta" | "phase" | "exchange" | "outcome"
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data"`
}

// Writer appends redacted JSONL lines to a single run file.
type Writer struct {
	mu      sync.Mutex
	f       *os.File
	dropped int
}

// NewWriter creates dir (0700) and the run file (0600) inside it.
func NewWriter(dir, name string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("record: creating run dir: %w", err)
	}
	// Run directory comes from -out; writing there is the point of the tool.
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- operator-supplied output path
	if err != nil {
		return nil, fmt.Errorf("record: opening run file: %w", err)
	}
	return &Writer{f: f}, nil
}

// Write serializes data under the given type, scrubs it, verifies the scrub,
// and appends it. On any redaction doubt the record is dropped and counted.
func (w *Writer) Write(typ string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("record: marshaling %s: %w", typ, err)
	}
	line, err := json.Marshal(Line{Type: typ, Time: time.Now().UTC(), Data: payload})
	if err != nil {
		return fmt.Errorf("record: marshaling envelope: %w", err)
	}
	clean := redact.Bytes(line)
	if err := redact.Verify(clean); err != nil {
		w.mu.Lock()
		w.dropped++
		w.mu.Unlock()
		return fmt.Errorf("record: dropped %s record: %w", typ, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(append(clean, '\n')); err != nil {
		return fmt.Errorf("record: writing: %w", err)
	}
	return nil
}

// Dropped reports how many records were discarded by fail-closed redaction.
func (w *Writer) Dropped() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dropped
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
