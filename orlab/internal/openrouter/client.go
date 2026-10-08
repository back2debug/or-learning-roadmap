package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Doer sends an HTTP request; satisfied by transport.Client and by the
// replay Doer.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to OpenRouter's supporting routes (/models, /generation).
type Client struct {
	Do        Doer
	BaseURL   string
	APIKey    string
	BodyLimit int64
	// ReconcileDelays overrides the /generation polling backoff (replay and
	// tests use tiny delays). Nil means the production default.
	ReconcileDelays []time.Duration
}

// Model is one catalog entry from GET /models, reduced to what preflight and
// analysis need.
type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
}

func (c *Client) get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.Do.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	limit := c.BodyLimit
	if limit <= 0 {
		limit = 32 << 20 // /models is large
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// Models fetches the full catalog.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	status, body, err := c.get(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("openrouter: GET /models: %w", err)
	}
	if status != http.StatusOK {
		return nil, ParseAPIError(status, body)
	}
	var env struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("openrouter: decoding /models: %w", err)
	}
	return env.Data, nil
}

// ErrGenerationPending signals the generation record has not materialized
// yet; poll again.
var ErrGenerationPending = errors.New("openrouter: generation record not ready")

// Generation fetches the authoritative post-hoc record for one generation.
func (c *Client) Generation(ctx context.Context, id string) (*GenerationRecord, error) {
	status, body, err := c.get(ctx, "/generation?id="+url.QueryEscape(id))
	if err != nil {
		return nil, fmt.Errorf("openrouter: GET /generation: %w", err)
	}
	if status == http.StatusNotFound {
		return nil, ErrGenerationPending
	}
	if status != http.StatusOK {
		return nil, ParseAPIError(status, body)
	}
	var env struct {
		Data *GenerationRecord `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("openrouter: decoding /generation: %w", err)
	}
	if env.Data == nil || env.Data.ID == "" {
		return nil, ErrGenerationPending
	}
	return env.Data, nil
}

// ReconcileGeneration polls Generation with bounded backoff, since the
// record populates asynchronously.
func (c *Client) ReconcileGeneration(ctx context.Context, id string) (*GenerationRecord, error) {
	delays := c.ReconcileDelays
	if delays == nil {
		delays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	}
	var lastErr error
	for _, d := range delays {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
		rec, err := c.Generation(ctx, id)
		if err == nil {
			return rec, nil
		}
		lastErr = err
		if !errors.Is(err, ErrGenerationPending) {
			return nil, err
		}
	}
	return nil, lastErr
}

// ClosestModels ranks catalog slugs by edit distance to slug, for the
// preflight "unknown slug, did you mean" message.
func ClosestModels(slug string, models []Model, n int) []string {
	type scored struct {
		id   string
		dist int
	}
	ranked := make([]scored, 0, len(models))
	for _, m := range models {
		d := levenshtein(strings.ToLower(slug), strings.ToLower(m.ID))
		// Substring matches are almost always what was meant.
		if strings.Contains(strings.ToLower(m.ID), strings.ToLower(slug)) {
			d = 0
		}
		ranked = append(ranked, scored{m.ID, d})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].dist < ranked[j].dist })
	out := make([]string, 0, n)
	for i := 0; i < len(ranked) && i < n; i++ {
		out = append(out, ranked[i].id)
	}
	return out
}

func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
