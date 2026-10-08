package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srvURL string, opts Options) *Client {
	t.Helper()
	opts.BaseURL = srvURL
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = time.Millisecond
	}
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	c.rand = func() float64 { return 0.5 }
	return c
}

func post(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader([]byte(`{"x":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRetriesOn429ThenSucceeds(t *testing.T) {
	var calls, retryEvents atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"x":1}` {
			t.Errorf("attempt %d body = %q, want replayed body", calls.Load()+1, b)
		}
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, Options{OnRetry: func(ev RetryEvent) {
		retryEvents.Add(1)
		if !ev.RetryAfter {
			t.Errorf("retry %d: RetryAfter = false, want true", ev.Attempt)
		}
	}})
	resp, err := c.Do(post(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || calls.Load() != 3 || retryEvents.Load() != 2 {
		t.Errorf("status=%d calls=%d retries=%d", resp.StatusCode, calls.Load(), retryEvents.Load())
	}
}

func TestRetriesExhaustedReturnsLastResponse(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, Options{MaxRetries: 2})
	resp, err := c.Do(post(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	if calls.Load() != 3 { // 1 + 2 retries
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, Options{})
	resp, err := c.Do(post(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestRefusesForeignRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, Options{})
	resp, err := c.Do(post(t, srv.URL))
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected redirect refusal, got nil error")
	}
	if !strings.Contains(err.Error(), "foreign host") {
		t.Errorf("err = %v, want foreign host refusal", err)
	}
}

func TestSameHostRedirectAllowed(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	c := newTestClient(t, srv.URL, Options{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/a", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestContextCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	req := post(t, srv.URL).WithContext(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := c.Do(req)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancel did not interrupt backoff sleep")
	}
}

// A cancelled context must abort the retry loop even when the failure was a
// transport error rather than a retryable status.
func TestContextCancelDuringTransportErrorBackoff(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close() // every dial now fails at the transport layer

	c := newTestClient(t, url, Options{MaxRetries: 100, BaseBackoff: time.Second, MaxBackoff: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	req := post(t, url).WithContext(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := c.Do(req)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("retry loop ignored cancellation for %v", elapsed)
	}
}

func TestBodyLimit(t *testing.T) {
	c := newTestClient(t, "https://openrouter.ai/api/v1", Options{BodyLimit: 10})
	_, err := c.ReadAll(strings.NewReader("0123456789ABC"))
	if err == nil || !strings.Contains(err.Error(), "body limit") {
		t.Errorf("err = %v, want body limit error", err)
	}
	b, err := c.ReadAll(strings.NewReader("012345678"))
	if err != nil || string(b) != "012345678" {
		t.Errorf("under-limit read: %q, %v", b, err)
	}
}

func TestRetryWaitHonorsRetryAfterSeconds(t *testing.T) {
	c := newTestClient(t, "https://openrouter.ai/api/v1", Options{MaxBackoff: time.Hour})
	resp := &http.Response{Header: http.Header{"Retry-After": {"7"}}}
	wait, fromHeader := c.retryWait(resp, 1)
	if wait != 7*time.Second || !fromHeader {
		t.Errorf("wait = %v fromHeader=%v", wait, fromHeader)
	}
}

func TestBackoffCapped(t *testing.T) {
	c := newTestClient(t, "https://openrouter.ai/api/v1", Options{BaseBackoff: time.Second, MaxBackoff: 2 * time.Second})
	c.rand = func() float64 { return 1.0 }
	if got := c.backoff(10); got > 2*time.Second {
		t.Errorf("backoff(10) = %v, want <= 2s", got)
	}
}
