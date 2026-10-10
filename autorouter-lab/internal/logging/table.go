package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"unicode"
)

// Column is one column of the console table: the attribute to show and how
// wide to make it.
type Column struct {
	Key   string
	Width int
}

// Table is a slog.Handler that prints records carrying the table's columns as
// aligned rows, and anything else as a plain line. It is the live console
// view; the JSONL sink stays the source of truth.
//
// Model names, task types and error messages come from API responses, so they
// are untrusted: every value is run through the redactor and then stripped of
// control characters, which keeps a hostile string from driving the terminal
// with escape sequences.
type Table struct {
	mu      *sync.Mutex
	w       io.Writer
	cols    []Column
	replace func([]string, slog.Attr) slog.Attr
	header  *bool
}

// NewTable builds a table handler writing to w.
func NewTable(w io.Writer, o Options, cols []Column) *Table {
	return &Table{mu: &sync.Mutex{}, w: w, cols: cols, replace: NewReplaceAttr(o), header: new(bool)}
}

// Enabled implements slog.Handler.
func (t *Table) Enabled(context.Context, slog.Level) bool { return true }

// WithAttrs implements slog.Handler; the table has no use for bound attributes.
func (t *Table) WithAttrs([]slog.Attr) slog.Handler { return t }

// WithGroup implements slog.Handler.
func (t *Table) WithGroup(string) slog.Handler { return t }

// Handle implements slog.Handler.
func (t *Table) Handle(_ context.Context, r slog.Record) error {
	vals := map[string]string{}
	var order []string
	r.Attrs(func(a slog.Attr) bool {
		a.Value = a.Value.Resolve()
		a = t.replace(nil, a)
		vals[a.Key] = clean(a.Value.String())
		order = append(order, a.Key)
		return true
	})

	var b strings.Builder
	if _, isRow := vals[t.cols[0].Key]; !isRow {
		b.WriteString(clean(r.Message))
		for _, k := range order {
			fmt.Fprintf(&b, "  %s=%s", k, vals[k])
		}
		b.WriteByte('\n')
	} else {
		t.mu.Lock()
		first := !*t.header
		*t.header = true
		t.mu.Unlock()
		if first {
			for _, c := range t.cols {
				fmt.Fprintf(&b, "%-*s  ", c.Width, strings.ToUpper(c.Key))
			}
			b.WriteByte('\n')
		}
		for _, c := range t.cols {
			fmt.Fprintf(&b, "%-*s  ", c.Width, fit(vals[c.Key], c.Width))
		}
		b.WriteByte('\n')
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := io.WriteString(t.w, b.String())
	return err
}

// clean replaces control and other non-printing characters.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
}

func fit(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "~"
}
