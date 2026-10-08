// Package runner orchestrates the nine phases for every
// (suite entry × model × dialect) combination, with bounded concurrency and
// explicit phase events.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/config"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/dialect"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/openrouter"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/record"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/redact"
	"github.com/back2debug/or-learning-roadmap/orlab/internal/transport"
)

// Doer sends one HTTP request: the live transport or the replayer.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Outcome is the full record of one request: what was built, what happened
// on the wire, and the normalized result. It is the unit the analyzer
// consumes.
type Outcome struct {
	EntryID      string                `json:"entry_id"`
	Kind         string                `json:"kind"`
	Model        string                `json:"model"`
	Dialect      string                `json:"dialect"`
	Attempt      int                   `json:"attempt"` // 1-based, for --repeat
	Streamed     bool                  `json:"streamed"`
	Path         string                `json:"path"`
	RequestBody  json.RawMessage       `json:"request_body,omitempty"`
	Notes        dialect.BuildNotes    `json:"notes"`
	Status       int                   `json:"status,omitempty"`
	TransportErr string                `json:"transport_err,omitempty"`
	APIError     *openrouter.APIError  `json:"api_error,omitempty"`
	Result       *dialect.Result       `json:"result,omitempty"`
	Events       []dialect.StreamEvent `json:"events,omitempty"`
	Total        time.Duration         `json:"total_ns"`
}

// RunData is everything a run produced, for analysis and reporting.
type RunData struct {
	RunID      string                      `json:"run_id"`
	StartedAt  time.Time                   `json:"started_at"`
	FinishedAt time.Time                   `json:"finished_at"`
	Models     []string                    `json:"models"`
	Catalog    map[string]openrouter.Model `json:"catalog,omitempty"`
	Outcomes   []Outcome                   `json:"outcomes"`
	Partial    bool                        `json:"partial"`
	DryRun     bool                        `json:"dry_run"`
}

type Runner struct {
	Cfg        *config.Config
	Do         Doer
	OR         *openrouter.Client
	Dialects   []dialect.Dialect
	Rec        *record.Writer
	Log        *slog.Logger
	Transcript io.Writer
	BodyLimit  int64
	// Replay softens preflight failures: analysis must be able to run from
	// cassettes that never recorded /models.
	Replay bool

	mu       sync.Mutex
	outcomes []Outcome
	tmu      sync.Mutex // serializes transcript writes across model groups
}

// Run executes the suite against the requested models.
func (r *Runner) Run(ctx context.Context, suite []dialect.PromptSpec, models []string) (*RunData, error) {
	data := &RunData{
		RunID:     time.Now().UTC().Format("20060102-150405"),
		StartedAt: time.Now().UTC(),
		Models:    models,
		DryRun:    r.Cfg.DryRun,
	}
	if r.BodyLimit <= 0 {
		r.BodyLimit = transport.DefaultBodyLimit
	}

	catalog, models, err := r.preflight(ctx, models)
	if err != nil {
		return nil, err
	}
	data.Catalog = catalog
	data.Models = models

	// Pinned entries (spec.Model set, e.g. the invalid-slug probe) run once;
	// the rest run per model. Each group runs its entries sequentially so no
	// model ever sees two concurrent requests; groups run in parallel under
	// the semaphore.
	var pinned, normal []dialect.PromptSpec
	for _, s := range suite {
		if s.Model != "" {
			pinned = append(pinned, s)
		} else {
			normal = append(normal, s)
		}
	}
	groups := make([][]dialect.PromptSpec, 0, len(models)+1)
	for _, m := range models {
		g := make([]dialect.PromptSpec, len(normal))
		for i, s := range normal {
			s.Model = m
			g[i] = s
		}
		groups = append(groups, g)
	}
	if len(pinned) > 0 {
		groups = append(groups, pinned)
	}

	sem := make(chan struct{}, r.Cfg.Concurrency)
	var wg sync.WaitGroup
	for _, group := range groups {
		wg.Add(1)
		go func(entries []dialect.PromptSpec) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			for _, spec := range entries {
				if ctx.Err() != nil {
					return
				}
				for attempt := 1; attempt <= r.Cfg.Repeat; attempt++ {
					r.runEntry(ctx, spec, attempt)
				}
			}
		}(group)
	}
	wg.Wait()

	r.mu.Lock()
	data.Outcomes = r.outcomes
	r.mu.Unlock()
	sortOutcomes(data.Outcomes)
	data.FinishedAt = time.Now().UTC()
	data.Partial = ctx.Err() != nil
	if data.Partial {
		r.Log.Warn("run interrupted; results are partial")
	}
	r.write("meta", data)
	return data, nil
}

func (r *Runner) preflight(ctx context.Context, models []string) (map[string]openrouter.Model, []string, error) {
	if r.Cfg.DryRun {
		r.phase("", "", "", "preflight", "skipped (dry-run)")
		return nil, models, nil
	}
	catalog, err := r.OR.Models(ctx)
	if err != nil {
		if r.Replay {
			r.phase("", "", "", "preflight", "no /models cassette; continuing without catalog")
			return nil, models, nil
		}
		return nil, nil, fmt.Errorf("preflight: %w", err)
	}
	byID := make(map[string]openrouter.Model, len(catalog))
	for _, m := range catalog {
		byID[m.ID] = m
	}

	out := map[string]openrouter.Model{}
	resolved := make([]string, 0, len(models))
	for _, slug := range models {
		if r.Cfg.Cheap {
			if free, ok := byID[slug+":free"]; ok {
				r.phase("", slug, "", "preflight", "cheap profile: using "+free.ID)
				slug = free.ID
			}
		}
		m, ok := byID[slug]
		if !ok {
			suggestions := openrouter.ClosestModels(slug, catalog, 3)
			return nil, nil, fmt.Errorf("preflight: unknown model %q; closest matches: %s",
				slug, strings.Join(suggestions, ", "))
		}
		out[slug] = m
		resolved = append(resolved, slug)
		r.phase("", slug, "", "preflight",
			fmt.Sprintf("ok: context=%d, %d supported parameters", m.ContextLength, len(m.SupportedParameters)))
	}
	return out, resolved, nil
}

// runEntry executes one suite entry on one model across all dialects.
func (r *Runner) runEntry(ctx context.Context, spec dialect.PromptSpec, attempt int) {
	// Streaming is the default because echo requires it; the buffered kind
	// and --no-stream force the buffered path. debug is still sent on
	// buffered requests — docs say it is ignored there, which the buffered
	// entry exists to confirm.
	spec.Stream = r.Cfg.Stream && spec.Kind != "buffered"
	spec.EchoUpstream = r.Cfg.EchoUpstream

	type built struct {
		req   dialect.Request
		notes dialect.BuildNotes
	}
	builds := map[string]built{}
	for _, d := range r.Dialects {
		req, notes, err := d.BuildRequest(spec)
		if err != nil {
			r.phase(spec.ID, spec.Model, d.Name(), "build", "FAILED: "+err.Error())
			r.addOutcome(Outcome{
				EntryID: spec.ID, Kind: string(spec.Kind), Model: spec.Model,
				Dialect: d.Name(), Attempt: attempt, TransportErr: "build: " + err.Error(),
			})
			continue
		}
		builds[d.Name()] = built{req, notes}
		r.phase(spec.ID, spec.Model, d.Name(), "build",
			fmt.Sprintf("%s, %d bytes", req.Path, len(req.Body)))
	}
	r.phase(spec.ID, spec.Model, "", "build.diff", keyDiff(builds["chat"].req.Body, builds["responses"].req.Body, builds["messages"].req.Body))

	for _, d := range r.Dialects {
		b, ok := builds[d.Name()]
		if !ok {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		out := r.execute(ctx, spec, d, b.req, attempt)
		out.Notes = b.notes
		r.addOutcome(out)
		r.write("outcome", out)
	}
}

// execute runs phases Send→Reconcile for one dialect request.
func (r *Runner) execute(ctx context.Context, spec dialect.PromptSpec, d dialect.Dialect, dreq dialect.Request, attempt int) Outcome {
	out := Outcome{
		EntryID: spec.ID, Kind: string(spec.Kind), Model: spec.Model,
		Dialect: d.Name(), Attempt: attempt, Streamed: spec.Stream,
		Path: dreq.Path, RequestBody: json.RawMessage(dreq.Body),
	}
	if r.Cfg.DryRun {
		r.phase(spec.ID, spec.Model, d.Name(), "send", "skipped (dry-run)")
		return out
	}

	rctx, cancel := context.WithTimeout(ctx, r.Cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, r.Cfg.BaseURL+dreq.Path, bytes.NewReader(dreq.Body))
	if err != nil {
		out.TransportErr = err.Error()
		return out
	}
	if r.Cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.Cfg.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpenRouter-Metadata", "enabled")
	for k, v := range dreq.Header {
		req.Header.Set(k, v)
	}

	r.phase(spec.ID, spec.Model, d.Name(), "send",
		fmt.Sprintf("POST %s (%d bytes), headers: %v", dreq.Path, len(dreq.Body), redact.Headers(req.Header)))

	start := time.Now()
	resp, err := r.Do.Do(req)
	if err != nil {
		out.TransportErr = err.Error()
		out.Total = time.Since(start)
		r.phase(spec.ID, spec.Model, d.Name(), "receive", "transport error: "+redact.String(err.Error()))
		return out
	}

	defer resp.Body.Close()

	out.Status = resp.StatusCode
	r.phase(spec.ID, spec.Model, d.Name(), "receive",
		fmt.Sprintf("status %d, rate-limit remaining=%q", resp.StatusCode, resp.Header.Get("X-RateLimit-Remaining")))

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(transport.LimitReader(resp.Body, r.BodyLimit))
		if readErr != nil {
			out.TransportErr = readErr.Error()
		}
		out.APIError = openrouter.ParseAPIError(resp.StatusCode, body)
		out.Total = time.Since(start)
		r.phase(spec.ID, spec.Model, d.Name(), "receive",
			fmt.Sprintf("error envelope: type=%q code=%q", out.APIError.ErrorType, out.APIError.Code))
		return out
	}

	var res *dialect.Result
	var events []dialect.StreamEvent
	if spec.Stream {
		res, events, err = d.ParseStream(dialect.NewStreamReader(transport.LimitReader(resp.Body, r.BodyLimit), start))
	} else {
		var body []byte
		body, err = io.ReadAll(transport.LimitReader(resp.Body, r.BodyLimit))
		if err == nil {
			res, err = d.ParseResponse(body)
		}
	}
	out.Total = time.Since(start)
	if err != nil {
		out.TransportErr = "parse: " + err.Error()
		out.Events = events
		r.phase(spec.ID, spec.Model, d.Name(), "normalize", "FAILED: "+redact.String(err.Error()))
		return out
	}
	res.Total = out.Total
	out.Result = res
	out.Events = events

	r.phase(spec.ID, spec.Model, d.Name(), "echo",
		fmt.Sprintf("%d upstream body(ies) captured", len(res.UpstreamBodies)))
	r.phase(spec.ID, spec.Model, d.Name(), "metadata", metadataSummary(res.RouterMetadata))
	r.phase(spec.ID, spec.Model, d.Name(), "normalize",
		fmt.Sprintf("stop=%s/%s text=%dB reasoning=%dB tools=%d usage=%s ttfb=%s total=%s",
			res.StopReason, res.NativeStopReason, len(res.Text), len(res.ReasoningText),
			len(res.ToolCalls), usageSummary(res.Usage), res.TTFB.Round(time.Millisecond), res.Total.Round(time.Millisecond)))

	r.reconcile(ctx, spec, d, res)
	return out
}

func (r *Runner) reconcile(ctx context.Context, spec dialect.PromptSpec, d dialect.Dialect, res *dialect.Result) {
	if res.GenerationID == "" {
		r.phase(spec.ID, spec.Model, d.Name(), "reconcile", "skipped: no generation id")
		return
	}
	rec, err := r.OR.ReconcileGeneration(ctx, res.GenerationID)
	switch {
	case err == nil:
		res.Reconciled = rec
		r.phase(spec.ID, spec.Model, d.Name(), "reconcile",
			fmt.Sprintf("ok: provider=%s native_prompt=%s native_completion=%s cost=%s",
				rec.ProviderName, fmtIntPtr(rec.NativeTokensPrompt), fmtIntPtr(rec.NativeTokensCompletion), fmtFloatPtr(rec.TotalCost)))
	case errors.Is(err, openrouter.ErrGenerationPending):
		r.phase(spec.ID, spec.Model, d.Name(), "reconcile", "record never materialized (gave up after backoff)")
	default:
		r.phase(spec.ID, spec.Model, d.Name(), "reconcile", "error: "+redact.String(err.Error()))
	}
}

func (r *Runner) addOutcome(o Outcome) {
	r.mu.Lock()
	r.outcomes = append(r.outcomes, o)
	r.mu.Unlock()
}

// phase emits one labeled phase event to the structured log, the human
// transcript, and the JSONL record.
func (r *Runner) phase(entry, model, dial, phase, msg string) {
	r.Log.Info("phase", "phase", phase, "entry", entry, "model", model, "dialect", dial, "msg", msg)
	parts := make([]string, 0, 3)
	for _, p := range []string{entry, model, dial} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	scope := strings.Join(parts, " ")
	if scope == "" {
		scope = "run"
	}
	r.tmu.Lock()
	fmt.Fprintf(r.Transcript, "[%s] %-10s %s\n", scope, strings.ToUpper(phase), msg)
	r.tmu.Unlock()
	r.write("phase", map[string]string{
		"entry": entry, "model": model, "dialect": dial, "phase": phase, "msg": msg,
	})
}

// write persists one record. A record dropped by fail-closed redaction is
// logged rather than swallowed; the writer also counts drops for the
// end-of-run summary.
func (r *Runner) write(typ string, data any) {
	if err := r.Rec.Write(typ, data); err != nil {
		r.Log.Error("record not written", "type", typ, "err", redact.String(err.Error()))
	}
}

// keyDiff summarizes which top-level body keys differ across the three
// dialects — the quick in-transcript version of the Build diff (the full
// diff lands in the analysis report).
func keyDiff(bodies ...[]byte) string {
	sets := make([]map[string]bool, len(bodies))
	all := map[string]bool{}
	for i, b := range bodies {
		sets[i] = map[string]bool{}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		for k := range m {
			sets[i][k] = true
			all[k] = true
		}
	}
	var shared, differing []string
	for k := range all {
		everywhere := true
		for _, s := range sets {
			if !s[k] {
				everywhere = false
				break
			}
		}
		if everywhere {
			shared = append(shared, k)
		} else {
			differing = append(differing, k)
		}
	}
	sort.Strings(shared)
	sort.Strings(differing)
	return fmt.Sprintf("shared keys: %s | dialect-specific: %s",
		strings.Join(shared, ","), strings.Join(differing, ","))
}

func metadataSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "openrouter_metadata absent"
	}
	var m struct {
		Strategy string `json:"strategy"`
		Attempt  int    `json:"attempt"`
		Summary  string `json:"summary"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "openrouter_metadata present (unparsed)"
	}
	return fmt.Sprintf("strategy=%s attempt=%d %s", m.Strategy, m.Attempt, m.Summary)
}

func usageSummary(u dialect.Usage) string {
	return fmt.Sprintf("p=%s c=%s r=%s cached=%s cost=%s",
		fmtIntPtr(u.PromptTokens), fmtIntPtr(u.CompletionTokens),
		fmtIntPtr(u.ReasoningTokens), fmtIntPtr(u.CachedTokens), fmtFloatPtr(u.Cost))
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}

func fmtFloatPtr(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%g", *p)
}

func sortOutcomes(out []Outcome) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.EntryID != b.EntryID {
			return a.EntryID < b.EntryID
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Dialect != b.Dialect {
			return a.Dialect < b.Dialect
		}
		return a.Attempt < b.Attempt
	})
}
