package dialect

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"time"
)

// lineReader is the production StreamReader: a bufio-backed line scanner with
// a wall clock anchored at request send time.
type lineReader struct {
	br    *bufio.Reader
	start time.Time
}

// NewStreamReader wraps an SSE body. start is when the request was sent, so
// Elapsed yields TTFB-compatible offsets. r should already be read-bounded by
// the transport.
func NewStreamReader(r io.Reader, start time.Time) StreamReader {
	return &lineReader{br: bufio.NewReader(r), start: start}
}

func (l *lineReader) ReadLine() ([]byte, error) {
	line, err := l.br.ReadBytes('\n')
	if len(line) > 0 {
		line = bytes.TrimRight(line, "\r\n")
		// A final unterminated line still counts as a line.
		if err == io.EOF {
			err = nil
		}
	}
	return line, err
}

func (l *lineReader) Elapsed() time.Duration { return time.Since(l.start) }

// sseRecord is one server-sent event: an optional event name and the joined
// data payload.
type sseRecord struct {
	event string
	data  []byte
	at    time.Duration // Elapsed at the record's first data line
}

// readSSE returns the next complete SSE record, skipping comment lines and
// blank keep-alives. io.EOF signals a cleanly exhausted stream.
func readSSE(r StreamReader) (*sseRecord, error) {
	rec := &sseRecord{}
	var dataLines [][]byte
	for {
		line, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(dataLines) > 0 {
				// Stream ended mid-record; deliver what we have.
				rec.data = bytes.Join(dataLines, []byte("\n"))
				return rec, nil
			}
			return nil, err
		}
		switch {
		case len(line) == 0:
			if len(dataLines) == 0 && rec.event == "" {
				continue // stray blank line between records
			}
			rec.data = bytes.Join(dataLines, []byte("\n"))
			return rec, nil
		case line[0] == ':':
			continue // comment / keep-alive (": OPENROUTER PROCESSING")
		case bytes.HasPrefix(line, []byte("event:")):
			rec.event = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			if len(dataLines) == 0 {
				rec.at = r.Elapsed()
			}
			dataLines = append(dataLines, bytes.TrimPrefix(bytes.TrimSpace(line[len("data:"):]), []byte(" ")))
		default:
			// Unknown field (id:, retry:, or garbage) — ignore per SSE spec.
		}
	}
}
