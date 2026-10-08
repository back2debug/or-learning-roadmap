// Package transport provides the HTTP client used for every network call:
// TLS 1.2+, redirects pinned to the API host, bounded retries with full
// jitter, and bounded response reads.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBodyLimit caps response body reads. Streams that legitimately exceed
// it should raise the cap via Options, not bypass it.
const DefaultBodyLimit = 8 << 20 // 8 MiB

// RetryEvent is reported before each retry sleep. Retry behavior is itself a
// finding, so the runner logs these as phase events.
type RetryEvent struct {
	Attempt    int // 1-based attempt that just failed
	Status     int // 0 when the failure was a transport error
	Err        error
	Wait       time.Duration
	RetryAfter bool // wait came from a Retry-After header
}

type Options struct {
	BaseURL     string        // requests may only ever talk (and redirect) to this host
	MaxRetries  int           // retries after the first attempt; default 3
	BaseBackoff time.Duration // default 500ms
	MaxBackoff  time.Duration // default 30s
	BodyLimit   int64         // default DefaultBodyLimit
	OnRetry     func(RetryEvent)
	// Transport overrides the underlying RoundTripper (tests only).
	Transport http.RoundTripper
}

// Client wraps http.Client with retry and read-bounding policy.
type Client struct {
	http *http.Client
	opts Options
	rand func() float64 // in [0,1); swappable for tests
}

func New(opts Options) (*Client, error) {
	base, err := url.Parse(opts.BaseURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("transport: invalid base URL %q", opts.BaseURL)
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = 500 * time.Millisecond
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.BodyLimit == 0 {
		opts.BodyLimit = DefaultBodyLimit
	}

	rt := opts.Transport
	if rt == nil {
		rt = &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		}
	}
	allowedHost := base.Host
	return &Client{
		http: &http.Client{
			Transport: rt,
			// No Timeout here: it would kill long-lived SSE streams. Deadlines
			// come from the request context.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Host != allowedHost {
					return fmt.Errorf("refusing redirect to foreign host %q", req.URL.Host)
				}
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
		opts: opts,
		rand: rand.Float64,
	}, nil
}

// BodyLimit returns the configured response read cap.
func (c *Client) BodyLimit() int64 { return c.opts.BodyLimit }

// Do sends req, retrying on 429/5xx and transport errors with exponential
// backoff and full jitter, honoring Retry-After. The request must have
// GetBody set (any bytes.Reader body does) so it can be replayed.
//
// On success the response body is NOT limited here — callers must wrap it
// with c.LimitBody. Streaming callers read incrementally; buffered callers
// use ReadAll.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if req.GetBody == nil {
				return nil, fmt.Errorf("transport: cannot retry request without GetBody: %w", lastErr)
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("transport: rewinding body for retry: %w", err)
			}
			req.Body = body
		}

		// The destination is the operator-supplied -base-url, which config
		// constrains to https (or a verified loopback host), and
		// CheckRedirect above refuses any redirect off that host.
		resp, err := c.http.Do(req) // #nosec G704 -- base URL validated in config; redirects host-pinned
		if err != nil {
			lastErr = err
			if req.Context().Err() != nil || attempt > c.opts.MaxRetries {
				return nil, fmt.Errorf("transport: %w", err)
			}
			if serr := c.sleep(req.Context(), RetryEvent{Attempt: attempt, Err: err, Wait: c.backoff(attempt)}); serr != nil {
				return nil, serr
			}
			continue
		}
		if !retryable(resp.StatusCode) || attempt > c.opts.MaxRetries {
			return resp, nil
		}

		wait, fromHeader := c.retryWait(resp, attempt)
		// Drain and close so the connection is reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.opts.BodyLimit))
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		ev := RetryEvent{Attempt: attempt, Status: resp.StatusCode, Wait: wait, RetryAfter: fromHeader}
		if err := c.sleep(req.Context(), ev); err != nil {
			return nil, err
		}
	}
}

// LimitBody wraps r so reads beyond the cap fail loudly instead of silently
// truncating.
func (c *Client) LimitBody(r io.Reader) io.Reader {
	return &limitedReader{r: r, n: c.opts.BodyLimit}
}

// LimitReader is LimitBody for callers that hold a cap but not a Client
// (e.g. the runner in replay mode).
func LimitReader(r io.Reader, n int64) io.Reader {
	return &limitedReader{r: r, n: n}
}

// ReadAll reads a whole body under the cap.
func (c *Client) ReadAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(c.LimitBody(r))
}

type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("transport: response exceeded body limit")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func (c *Client) retryWait(resp *http.Response, attempt int) (time.Duration, bool) {
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, c.opts.MaxBackoff), true
		}
		if t, err := http.ParseTime(h); err == nil {
			if d := time.Until(t); d > 0 {
				return min(d, c.opts.MaxBackoff), true
			}
		}
	}
	return c.backoff(attempt), false
}

// backoff returns full-jitter exponential backoff: uniform in [0, base*2^n].
func (c *Client) backoff(attempt int) time.Duration {
	ceil := min(c.opts.BaseBackoff<<uint(attempt-1), c.opts.MaxBackoff)
	return time.Duration(c.rand() * float64(ceil))
}

func (c *Client) sleep(ctx context.Context, ev RetryEvent) error {
	if c.opts.OnRetry != nil {
		c.opts.OnRetry(ev)
	}
	t := time.NewTimer(ev.Wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
