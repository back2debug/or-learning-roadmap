package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// output is where all human-readable exploration output (request/response
// JSON, struct inspections, the comparison) is written. main() replaces it
// with a writer that tees to stdout and the log file.
var output io.Writer = os.Stdout

// openLogFile creates (or truncates) the run log in the current working
// directory and returns the open file plus its absolute path, so the user
// knows exactly where to find it.
func openLogFile() (*os.File, string, error) {
	file, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("create log file %s: %w", logFileName, err)
	}
	absolutePath, err := filepath.Abs(logFileName)
	if err != nil {
		absolutePath = logFileName
	}
	return file, absolutePath, nil
}

// newLogger builds the structured logger used for flow/debug/error events,
// writing to the given destination. Set LOG_LEVEL=debug for verbose output.
func newLogger(destination io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(destination, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// redactKey shows only the first redactKeyPrefix characters of a secret.
// It is the ONLY function allowed to render the API key for display.
func redactKey(key string) string {
	if len(key) <= redactKeyPrefix {
		return "***"
	}
	return key[:redactKeyPrefix] + "...***"
}

// prettyJSON re-indents raw JSON for human inspection. Malformed input is
// returned as-is rather than panicking.
func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// printSection emits a visually distinct separator for each part of the demo.
func printSection(title string) {
	fmt.Fprintf(output, "\n%s\n=== %s ===\n%s\n", divider, title, divider)
}

const divider = "======================================================================"
