// Package transport sends trials to OpenRouter and records exactly what went
// on the wire and what came back.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/logging"
)

// maxBody bounds how much of a request or response is buffered. Responses are
// untrusted input, so the size is capped rather than assumed.
const maxBody = 8 << 20

// ErrDryRun is returned by Capture in dry-run mode after recording the request
// it would have sent.
var ErrDryRun = errors.New("dry run: request rendered but not sent")

// Exchange is one captured HTTP round trip.
//
// The bodies are the exact bytes the SDK handed to net/http and the exact
// bytes net/http handed back. Two caveats follow from sitting above
// http.Transport rather than on the socket: headers the transport adds itself
// (Host, Content-Length, Accept-Encoding) are not in RequestHeader, and a
// gzip-encoded response is already decompressed here.
type Exchange struct {
	Method         string
	URL            string
	RequestHeader  http.Header
	RequestBody    []byte
	Status         int
	ResponseHeader http.Header
	ResponseBody   []byte
	Started        time.Time
	// Latency runs from just before the request is sent until the response
	// body has been read in full.
	Latency time.Duration
	// Attempts counts round trips made under this exchange's context. More
	// than one means something retried behind the harness's back.
	Attempts int
	// DryRun is true when the request was rendered but deliberately not sent.
	DryRun bool
	// Blocked is the guard error that stopped the request, if one did. It is
	// kept here because the SDK does not reliably preserve error chains.
	Blocked error
}

type exchangeKey struct{}

// WithExchange attaches an empty Exchange to ctx. Capture fills it in when a
// request made with that context passes through, which ties a capture to its
// trial without any shared state between concurrent trials.
func WithExchange(ctx context.Context) (context.Context, *Exchange) {
	ex := &Exchange{}
	return context.WithValue(ctx, exchangeKey{}, ex), ex
}

// Guard inspects a fully rendered request and returns an error to block it.
type Guard func(req *http.Request, body []byte) error

// Capture is an http.RoundTripper that records every exchange and runs guards
// against the rendered request before anything is sent.
//
// It exists because the OpenRouter SDK is generated and in beta: whether a
// field reached the wire is a question for the bytes, not the type system.
// It sees credentials in the clear, so nothing here writes to a sink; the
// Exchange is only ever logged through logging's redacting handler.
type Capture struct {
	// Base performs the real round trip.
	Base http.RoundTripper
	// Guards run in order; the first error blocks the request.
	Guards []Guard
	// DryRun records the request and returns ErrDryRun instead of sending.
	DryRun bool
}

// RoundTrip implements http.RoundTripper.
func (c *Capture) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
		cerr := req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("capture: read request body: %w", err)
		}
		if cerr != nil {
			return nil, fmt.Errorf("capture: close request body: %w", cerr)
		}
		if len(b) > maxBody {
			return nil, fmt.Errorf("capture: request body exceeds %d bytes", maxBody)
		}
		body = b
	}

	ex, _ := req.Context().Value(exchangeKey{}).(*Exchange)
	if ex == nil {
		ex = &Exchange{}
	}
	ex.Attempts++
	ex.Method = req.Method
	ex.URL = req.URL.String()
	ex.RequestHeader = req.Header.Clone()
	ex.RequestBody = body

	for _, g := range c.Guards {
		if err := g(req, body); err != nil {
			ex.Blocked = fmt.Errorf("request blocked: %w", err)
			return nil, ex.Blocked
		}
	}
	if c.DryRun {
		ex.DryRun = true
		return nil, ErrDryRun
	}

	// A RoundTripper must not modify the caller's request, so the buffered
	// body goes on a clone.
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))

	ex.Started = time.Now()
	resp, err := c.Base.RoundTrip(out)
	if err != nil {
		ex.Latency = time.Since(ex.Started)
		return nil, err
	}
	ex.Status = resp.StatusCode
	ex.ResponseHeader = resp.Header.Clone()

	rb, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	cerr := resp.Body.Close()
	ex.Latency = time.Since(ex.Started)
	ex.ResponseBody = rb
	if err != nil {
		return nil, fmt.Errorf("capture: read response body: %w", err)
	}
	if cerr != nil {
		return nil, fmt.Errorf("capture: close response body: %w", cerr)
	}
	if len(rb) > maxBody {
		return nil, fmt.Errorf("capture: response body exceeds %d bytes", maxBody)
	}
	resp.Body = io.NopCloser(bytes.NewReader(rb))
	return resp, nil
}

// LogValue renders the exchange for slog. Headers become one attribute per
// header so the redactor can match on header names, and bodies are typed as
// logging.Body so they are dropped unless bodies are enabled.
func (e *Exchange) LogValue() slog.Value {
	if e == nil {
		return slog.Value{}
	}
	return slog.GroupValue(
		slog.String("method", e.Method),
		slog.String("url", e.URL),
		slog.Int("attempts", e.Attempts),
		slog.Bool("dry_run", e.DryRun),
		slog.Any("request_headers", headerValue(e.RequestHeader)),
		slog.Any("request_body", logging.Body(e.RequestBody)),
		slog.Int("status", e.Status),
		slog.Any("response_headers", headerValue(e.ResponseHeader)),
		slog.Any("response_body", logging.Body(e.ResponseBody)),
		slog.Int64("latency_ms", e.Latency.Milliseconds()),
	)
}

func headerValue(h http.Header) slog.Value {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	slices.Sort(names)
	attrs := make([]slog.Attr, 0, len(names))
	for _, k := range names {
		attrs = append(attrs, slog.String(k, strings.Join(h[k], ", ")))
	}
	return slog.GroupValue(attrs...)
}
