package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// HTTPDoer is the minimal HTTP surface the client needs. It exists so tests
// can substitute a mock transport (see client_test.go).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a hardened OpenRouter API client.
type Client struct {
	httpClient HTTPDoer
	apiKey     string
	baseURL    string
	logger     *slog.Logger
}

// NewClient builds a Client from the OPENROUTER_API_KEY environment variable.
// It panics if the key is not set — the program cannot do anything useful
// without credentials, and an empty key must never be sent.
func NewClient(logger *slog.Logger) *Client {
	apiKey := os.Getenv(apiKeyEnvVar)
	if apiKey == "" {
		panic(fmt.Sprintf("environment variable %s is not set", apiKeyEnvVar))
	}
	logger.Info("OpenRouter client initialized", "api_key", redactKey(apiKey))

	transport := &http.Transport{
		DialContext:     (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, // verification stays ON
		MaxConnsPerHost: maxConnsPerHost,
		MaxIdleConns:    maxConnsPerHost,
	}
	httpClient := &http.Client{
		Transport: transport,
		// The API never legitimately redirects; following one could leak the
		// Authorization header to an unexpected host.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("refusing to follow redirect to %s", req.URL)
		},
	}
	return &Client{
		httpClient: httpClient,
		apiKey:     apiKey,
		baseURL:    openRouterBaseURL,
		logger:     logger,
	}
}

// RedactedKey returns the API key safe for display in logs.
func (c *Client) RedactedKey() string { return redactKey(c.apiKey) }

// CreateChatCompletion sends a chat completions request with bounded retries
// for transient failures. It returns the parsed response and the raw body
// (for pretty-printing).
func (c *Client) CreateChatCompletion(ctx context.Context, request *ChatRequest) (*ChatResponse, []byte, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := baseRetryDelay << (attempt - 1) // 1s, 2s, 4s
			c.logger.Warn("retrying request", "attempt", attempt, "delay", delay.String())
			select {
			case <-ctx.Done():
				return nil, nil, fmt.Errorf("retry aborted: %w", ctx.Err())
			case <-time.After(delay):
			}
		}
		response, raw, err := c.doOnce(ctx, body)
		if err == nil {
			return response, raw, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, nil, err
		}
		c.logger.Debug("transient failure", "error", err)
	}
	return nil, nil, fmt.Errorf("request failed after %d retries: %w", maxRetries, lastErr)
}

// doOnce performs a single HTTP round trip with its own timeout.
func (c *Client) doOnce(ctx context.Context, body []byte) (*ChatResponse, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, c.baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	// The API key travels ONLY in this header, never in the body or URL.
	httpRequest.Header.Set(headerAuthorization, "Bearer "+c.apiKey)
	httpRequest.Header.Set(headerContentType, contentTypeJSON)
	httpRequest.Header.Set(headerUserAgent, userAgent)
	httpRequest.Header.Set(headerTitle, "openrouter-tool-calling-explorer")

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("http request: %w", err)
	}
	defer func() {
		if httpResponse.Body != nil {
			_ = httpResponse.Body.Close()
		}
	}()

	requestID := httpResponse.Header.Get(headerRequestID)
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("read response body (request_id=%s): %w", requestID, err)
	}

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode > 299 {
		return nil, nil, newAPIStatusError(httpResponse.StatusCode, requestID, raw)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	var parsed ChatResponse
	if err := decoder.Decode(&parsed); err != nil {
		return nil, nil, fmt.Errorf("decode response (request_id=%s): %w", requestID, err)
	}
	if len(parsed.Choices) == 0 {
		return nil, nil, fmt.Errorf("response has no choices (request_id=%s, id=%s)", requestID, parsed.ID)
	}
	c.logger.Debug("request succeeded", "request_id", requestID, "response_id", parsed.ID)
	return &parsed, raw, nil
}

// newAPIStatusError parses the error envelope without exposing the full
// response body in logs or error strings.
func newAPIStatusError(status int, requestID string, raw []byte) error {
	var envelope apiErrorEnvelope
	message := "(unparseable error body)"
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		message = truncate(envelope.Error.Message, 200)
	}
	return &APIStatusError{StatusCode: status, RequestID: requestID, Message: message}
}

// isRetryable classifies errors: 429/5xx and transport failures are
// transient; context cancellation and 4xx are permanent.
func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *APIStatusError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	// Remaining cases are transport-level (dial, TLS, reset) — retry them.
	return true
}

// truncate limits a string for safe logging.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
