package record

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Exchange is one recorded HTTP exchange — the replay cassette unit.
// Request and response bodies are stored as text (both dialect JSON and SSE
// transcripts are UTF-8); headers are stored post-redaction.
type Exchange struct {
	Key         string              `json:"key"`
	Method      string              `json:"method"`
	Path        string              `json:"path"` // path + query, no host
	RequestBody string              `json:"request_body,omitempty"`
	Status      int                 `json:"status"`
	RespHeaders map[string][]string `json:"resp_headers,omitempty"`
	RespBody    string              `json:"resp_body,omitempty"`
}

// ExchangeKey identifies an exchange by what was sent, so replay can match a
// rebuilt request to its recorded response.
func ExchangeKey(method, pathAndQuery string, body []byte) string {
	h := sha256.New()
	// hash.Hash.Write never returns an error.
	_, _ = io.WriteString(h, method+" "+pathAndQuery+" ")
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// Replayer serves recorded exchanges as HTTP responses, keyed by request
// shape. It satisfies the Doer interfaces defined by consumers.
type Replayer struct {
	exchanges map[string]Exchange
}

// LoadReplayer reads every *.jsonl file in dir and indexes its exchange
// records.
func LoadReplayer(dir string) (*Replayer, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("record: no *.jsonl cassettes in %q", dir)
	}
	r := &Replayer{exchanges: map[string]Exchange{}}
	for _, file := range files {
		if err := r.loadFile(file); err != nil {
			return nil, err
		}
	}
	if len(r.exchanges) == 0 {
		return nil, fmt.Errorf("record: no exchange records found in %q", dir)
	}
	return r, nil
}

func (r *Replayer) loadFile(path string) error {
	// Cassette paths come from -replay, an operator-supplied directory.
	f, err := os.Open(path) // #nosec G304 -- operator-supplied replay path
	if err != nil {
		return fmt.Errorf("record: opening cassette: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for sc.Scan() {
		var line Line
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil || line.Type != "exchange" {
			continue
		}
		var ex Exchange
		if err := json.Unmarshal(line.Data, &ex); err != nil {
			continue
		}
		r.exchanges[ex.Key] = ex
	}
	return sc.Err()
}

// Len reports how many exchanges are indexed.
func (r *Replayer) Len() int { return len(r.exchanges) }

// Do matches req against the recorded exchanges. A missing exchange is an
// error, not a fabricated response.
func (r *Replayer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	pathq := req.URL.Path + querySuffix(req.URL.RawQuery)
	ex, ok := r.exchanges[ExchangeKey(req.Method, pathq, body)]
	if !ok {
		return nil, fmt.Errorf("record: no cassette for %s %s (body %d bytes)", req.Method, pathq, len(body))
	}
	resp := &http.Response{
		StatusCode: ex.Status,
		Status:     fmt.Sprintf("%d %s", ex.Status, http.StatusText(ex.Status)),
		Header:     http.Header(ex.RespHeaders),
		Body:       io.NopCloser(strings.NewReader(ex.RespBody)),
		Request:    req,
	}
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	return resp, nil
}

func querySuffix(raw string) string {
	if raw == "" {
		return ""
	}
	return "?" + raw
}

// PathAndQuery normalizes a request URL the same way recording does, so both
// sides of the cassette agree on keys.
func PathAndQuery(req *http.Request) string {
	return req.URL.Path + querySuffix(req.URL.RawQuery)
}

// CaptureBody wraps an http.Response body so the bytes stream through to the
// caller while being copied for the cassette; done fires exactly once with
// everything read, when the body hits EOF or is closed. Streaming timing is
// preserved because nothing is buffered ahead of the reader.
func CaptureBody(rc io.ReadCloser, done func([]byte)) io.ReadCloser {
	return &capture{rc: rc, done: done}
}

type capture struct {
	rc    io.ReadCloser
	buf   bytes.Buffer
	done  func([]byte)
	fired bool
}

func (c *capture) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.buf.Write(p[:n])
	}
	if err == io.EOF {
		c.fire()
	}
	return n, err
}

func (c *capture) Close() error {
	err := c.rc.Close()
	c.fire()
	return err
}

func (c *capture) fire() {
	if c.fired {
		return
	}
	c.fired = true
	if c.done != nil {
		c.done(c.buf.Bytes())
	}
}
