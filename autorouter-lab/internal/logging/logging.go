package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// logMode is owner read/write only: trial logs can hold prompts, model output
// and response headers.
const logMode os.FileMode = 0o600

// ErrLogMode means an existing log file is more permissive than logMode.
var ErrLogMode = errors.New("log file permissions too open")

// OpenLog opens the append-only JSONL sink.
//
// OpenFile applies the mode only when it creates the file, so an existing file
// keeps whatever permissions it had. The mode is therefore checked after
// opening, and a file that is too open is refused rather than quietly fixed.
func OpenLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, logMode) // #nosec G304 -- path comes from the operator's flag
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("log %s is not a regular file", path)
	}
	if err == nil && info.Mode().Perm()&^logMode != 0 {
		err = fmt.Errorf("%w: %s is %#o, want %#o; run `chmod 600 %s`", ErrLogMode, path, info.Mode().Perm(), logMode, path)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// NewJSON returns the JSONL logger: one object per record, the source of truth.
func NewJSON(w io.Writer, o Options) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: NewReplaceAttr(o)}))
}

// NewConsole returns the human-readable logger. It is a separate handler from
// the JSONL sink but shares the same redaction.
func NewConsole(w io.Writer, o Options) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{ReplaceAttr: NewReplaceAttr(o)}))
}

// ErrNotIgnored means git would track the given path.
var ErrNotIgnored = errors.New("path is not ignored by git")

// EnsureIgnored refuses a path that git would track. Run data can hold
// prompts, model output and response headers, so every command checks this
// before writing its first byte. Outside a git work tree there is nothing to
// leak into, so that case is allowed with a warning.
func EnsureIgnored(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- fixed command; path is the operator's own flag value.
	err := exec.CommandContext(ctx, "git", "check-ignore", "-q", "--", path).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return fmt.Errorf("%w: %s; fix .gitignore before writing run data", ErrNotIgnored, path)
	default:
		slog.Warn("could not check git ignore rules; continuing", "path", path, "error", err)
		return nil
	}
}
