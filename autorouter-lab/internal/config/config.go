package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"
)

// RouterSelector is what an experiment may put in its router field. "both"
// expands to one cell per track with identical settings, which is how a
// head-to-head comparison is declared: the two cells cannot drift apart the
// way two copy-pasted experiments could.
type RouterSelector string

const RouterBoth RouterSelector = "both"

// TransportKind selects the API surface a cell is sent over.
type TransportKind string

const (
	TransportChatCompletions TransportKind = "chat_completions"
	TransportMessages        TransportKind = "messages"
)

// Stickiness controls the session identity sent with each trial.
//
// The router remembers the model a conversation used and prefers it on later
// turns, identifying the conversation by an explicit session_id or by a
// fingerprint of the messages. It is a preference, not a pin, so repeating a
// prompt across cells contaminates results partially and non-obviously.
type Stickiness string

const (
	// StickinessIsolate sends a unique session_id per trial, so every trial
	// is routed fresh. It is the default and the only mode allowed in a
	// head-to-head comparison.
	StickinessIsolate Stickiness = "isolate"
	// StickinessSticky sends one fixed session_id for the whole cell, to
	// study the preference deliberately.
	StickinessSticky Stickiness = "sticky"
	// StickinessImplicit sends no session_id, to observe fingerprinting.
	StickinessImplicit Stickiness = "implicit"
)

// CostTier is a named cost band, cheapest to most capable. A tier is a band,
// not a ceiling: models cheaper than the band are excluded as well as more
// expensive ones.
type CostTier string

// CostTiers is the documented set, verified against the live Auto Router docs
// and the OpenAPI-generated SDK on 2026-10-09.
var CostTiers = []CostTier{"low", "medium", "high", "xhigh", "max"}

const (
	// CostQualityTradeoffMin and Max bound the deprecated numeric dial. The
	// range comes from the SDK's spec comment (0-10, higher favours cheaper
	// models); the docs page states no range.
	CostQualityTradeoffMin = 0
	CostQualityTradeoffMax = 10

	// MaxSessionIDLen is the documented session_id limit.
	MaxSessionIDLen = 256

	maxRepeats   = 100
	maxWait      = 1800
	maxSequence  = 20
	maxMaxTokens = 4096
	maxPatterns  = 1024
	maxPattern   = 1024
)

// File is the experiments YAML document.
type File struct {
	Version     int          `yaml:"version"`
	Defaults    Defaults     `yaml:"defaults"`
	Experiments []Experiment `yaml:"experiments"`
}

// Defaults apply to any experiment that leaves the field unset.
type Defaults struct {
	Repeats        *int `yaml:"repeats"`
	MaxTokens      *int `yaml:"max_tokens"`
	TimeoutSeconds *int `yaml:"timeout_seconds"`
	// MaxPrice is applied to every cell that does not set its own
	// provider.max_price. It is defence in depth for spend: the router picks
	// a model first, then this filters that model's endpoints, so a cell
	// whose chosen model has no endpoint under the cap fails instead of
	// overspending. That failure is intended and is never retried without
	// the cap.
	MaxPrice *MaxPrice `yaml:"max_price"`
}

// Experiment is one row of the matrix. Optional fields are pointers so that
// "unset" and "set to the zero value" stay distinguishable all the way to the
// wire; that matters most for cost_quality_tradeoff, where 0 is a real dial
// position that a plain int with omitempty would silently drop.
type Experiment struct {
	ID          string         `yaml:"id"`
	Description string         `yaml:"description"`
	Router      RouterSelector `yaml:"router"`
	Transport   TransportKind  `yaml:"transport"`
	PromptID    string         `yaml:"prompt_id"`
	// PromptSequence replaces PromptID for cells that change task within one
	// session: each entry is sent as its own single-turn request, in order.
	PromptSequence []string   `yaml:"prompt_sequence"`
	Stickiness     Stickiness `yaml:"stickiness"`
	// SessionID fixes the session for a sticky cell. Left unset, a sticky
	// cell gets one generated id per run. Sticky experiments that share a
	// session_id form one session and run in file order, which is how a
	// router switch inside a session is expressed.
	SessionID *string `yaml:"session_id"`
	// WaitBeforeSeconds pauses before the cell's first trial, to study how
	// the preference decays (sticky sessions expire after 10 idle minutes).
	WaitBeforeSeconds *int `yaml:"wait_before_seconds"`
	Repeats           *int `yaml:"repeats"`
	MaxTokens         *int `yaml:"max_tokens"`
	// AllowCostConflict must be true to send cost_tier and
	// cost_quality_tradeoff together. cost_tier wins when both are sent, so
	// sending both is only ever intended in the precedence experiment.
	AllowCostConflict bool `yaml:"allow_cost_conflict"`
	// ExpectStatus declares the HTTP status the cell should produce, 200 when
	// unset. A cell built to fail (an allow-list nothing can match, a price
	// cap nothing can meet) sets 404, and the 404 is then the passing result.
	ExpectStatus *int      `yaml:"expect_status"`
	Plugin       *Plugin   `yaml:"plugin"`
	Provider     *Provider `yaml:"provider"`
}

// Plugin is the per-request Auto Router configuration. The plugin id is never
// configurable here: it is derived from the router by Resolve.
type Plugin struct {
	AllowedModels  []string  `yaml:"allowed_models"`
	ExcludedModels []string  `yaml:"excluded_models"`
	CostTier       *CostTier `yaml:"cost_tier"`
	// CostQualityTradeoff is the deprecated numeric dial, still accepted
	// by the API for backwards compatibility.
	CostQualityTradeoff *int `yaml:"cost_quality_tradeoff"`
}

// Provider is the provider-routing block. The Auto Router resolves a model
// before provider routing runs, so these fields filter the chosen model's
// endpoints rather than the choice of model.
type Provider struct {
	Order             []string  `yaml:"order"`
	Only              []string  `yaml:"only"`
	Ignore            []string  `yaml:"ignore"`
	Sort              *string   `yaml:"sort"`
	MaxPrice          *MaxPrice `yaml:"max_price"`
	AllowFallbacks    *bool     `yaml:"allow_fallbacks"`
	RequireParameters *bool     `yaml:"require_parameters"`
	DataCollection    *string   `yaml:"data_collection"`
	ZDR               *bool     `yaml:"zdr"`
}

// MaxPrice caps what a request may cost: prompt and completion are USD per
// million tokens, request and image are USD each. If no endpoint of the
// selected model fits under the cap the request fails, which is the intended
// outcome.
type MaxPrice struct {
	Prompt     *float64 `yaml:"prompt"`
	Completion *float64 `yaml:"completion"`
	Request    *float64 `yaml:"request"`
	Image      *float64 `yaml:"image"`
}

// PromptFile is the shared prompt library document.
type PromptFile struct {
	Version int      `yaml:"version"`
	Prompts []Prompt `yaml:"prompts"`
}

// Prompt is one library entry. ExpectedTaskType is a hypothesis recorded for
// analysis, never an assertion: what the classifier returns is the finding.
type Prompt struct {
	ID               string `yaml:"id"`
	ExpectedTaskType string `yaml:"expected_task_type"`
	Text             string `yaml:"text"`
}

// Cell is one experiment resolved against one router: the unit the runner
// repeats.
type Cell struct {
	ID         string
	Experiment Experiment
	Target     Target
	// Prompts is the cell's prompt sequence; a plain prompt_id gives one.
	Prompts   []Prompt
	Repeats   int
	MaxTokens int
	Timeout   int // seconds
	// HeadToHead is true when the experiment asked for both routers.
	HeadToHead bool
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// decodeStrict decodes exactly one YAML document, rejecting unknown keys.
//
// The strictness is the point of the project, not a style choice: this harness
// exists to detect silently ignored configuration, and a mistyped key decoding
// to a zero value would produce that same failure inside the harness.
func decodeStrict(r io.Reader, v any) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("document is empty")
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("more than one YAML document; only one is allowed")
	}
	return nil
}

func loadFile(path string, v any) (err error) {
	f, err := os.Open(path) // #nosec G304 -- path comes from the operator's flag
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", path, cerr)
		}
	}()
	if err := decodeStrict(f, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Load reads both YAML files, validates them, and expands the experiments into
// cells. Any problem is an error; nothing is defaulted around.
func Load(experimentsPath, promptsPath string) ([]Cell, error) {
	var pf PromptFile
	if err := loadFile(promptsPath, &pf); err != nil {
		return nil, err
	}
	var ef File
	if err := loadFile(experimentsPath, &ef); err != nil {
		return nil, err
	}
	return Expand(ef, pf)
}

// Expand validates both documents and returns one cell per (experiment,
// router). All validation problems are reported together.
func Expand(ef File, pf PromptFile) ([]Cell, error) {
	var errs []error
	addf := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	prompts := map[string]Prompt{}
	if pf.Version != 1 {
		addf("prompts: version must be 1, got %d", pf.Version)
	}
	if len(pf.Prompts) == 0 {
		addf("prompts: no prompts defined")
	}
	for i, p := range pf.Prompts {
		switch {
		case !idPattern.MatchString(p.ID):
			addf("prompts[%d]: id %q must match %s", i, p.ID, idPattern)
		case p.Text == "":
			addf("prompt %q: text is required", p.ID)
		default:
			if _, dup := prompts[p.ID]; dup {
				addf("prompt %q: duplicate id", p.ID)
			}
			prompts[p.ID] = p
		}
	}

	if ef.Version != 1 {
		addf("experiments: version must be 1, got %d", ef.Version)
	}
	if len(ef.Experiments) == 0 {
		addf("experiments: no experiments defined")
	}
	checkRange := func(where, name string, v *int, lo, hi int) {
		if v != nil && (*v < lo || *v > hi) {
			addf("%s: %s must be %d..%d, got %d", where, name, lo, hi, *v)
		}
	}
	checkRange("defaults", "repeats", ef.Defaults.Repeats, 1, maxRepeats)
	checkRange("defaults", "max_tokens", ef.Defaults.MaxTokens, 1, maxMaxTokens)
	checkRange("defaults", "timeout_seconds", ef.Defaults.TimeoutSeconds, 1, 600)
	errs = append(errs, validateMaxPrice("defaults", ef.Defaults.MaxPrice)...)

	var cells []Cell
	seen := map[string]bool{}
	for i, e := range ef.Experiments {
		where := fmt.Sprintf("experiment %q", e.ID)
		if !idPattern.MatchString(e.ID) {
			addf("experiments[%d]: id %q must match %s", i, e.ID, idPattern)
			continue
		}
		if seen[e.ID] {
			addf("%s: duplicate id", where)
		}
		seen[e.ID] = true

		var routers []Router
		switch e.Router {
		case RouterSelector(RouterAuto), RouterSelector(RouterAutoBeta):
			routers = []Router{Router(e.Router)}
		case RouterBoth:
			routers = Routers()
		default:
			addf("%s: router must be auto, auto-beta or both, got %q", where, e.Router)
		}
		if e.Transport != TransportChatCompletions && e.Transport != TransportMessages {
			addf("%s: transport must be chat_completions or messages, got %q", where, e.Transport)
		}
		var seq []Prompt
		ids := e.PromptSequence
		switch {
		case e.PromptID != "" && len(ids) > 0:
			addf("%s: set prompt_id or prompt_sequence, not both", where)
		case e.PromptID != "":
			ids = []string{e.PromptID}
		case len(ids) == 0:
			addf("%s: prompt_id or prompt_sequence is required", where)
		case len(ids) > maxSequence:
			addf("%s: prompt_sequence has %d entries, limit is %d", where, len(ids), maxSequence)
		}
		for _, id := range ids {
			p, ok := prompts[id]
			if !ok {
				addf("%s: prompt %q is not in the prompt library", where, id)
				continue
			}
			seq = append(seq, p)
		}

		if e.Stickiness == "" {
			e.Stickiness = StickinessIsolate
		}
		switch e.Stickiness {
		case StickinessIsolate, StickinessSticky, StickinessImplicit:
		default:
			addf("%s: stickiness must be isolate, sticky or implicit, got %q", where, e.Stickiness)
		}
		// A remembered model from one router could be inherited by the
		// other, so a comparison is only valid when every trial routes fresh.
		if e.Router == RouterBoth && e.Stickiness != StickinessIsolate {
			addf("%s: router both is a head-to-head comparison and requires stickiness isolate, got %q", where, e.Stickiness)
		}
		if e.SessionID != nil {
			if e.Stickiness != StickinessSticky {
				addf("%s: session_id is only meaningful with stickiness sticky", where)
			}
			if n := len(*e.SessionID); n == 0 || n > MaxSessionIDLen {
				addf("%s: session_id must be 1..%d characters, got %d", where, MaxSessionIDLen, n)
			}
		}

		checkRange(where, "wait_before_seconds", e.WaitBeforeSeconds, 0, maxWait)
		checkRange(where, "expect_status", e.ExpectStatus, 200, 599)
		// A sequence only means something inside a session; under isolate
		// each entry would be routed independently anyway.
		if len(e.PromptSequence) > 0 && e.Stickiness == StickinessIsolate {
			addf("%s: prompt_sequence needs stickiness sticky or implicit", where)
		}
		if e.Provider == nil && ef.Defaults.MaxPrice != nil {
			e.Provider = &Provider{}
		}
		if e.Provider != nil && e.Provider.MaxPrice == nil {
			p := *e.Provider
			p.MaxPrice = ef.Defaults.MaxPrice
			e.Provider = &p
		}

		repeats := pick(e.Repeats, ef.Defaults.Repeats, 1)
		maxTokens := pick(e.MaxTokens, ef.Defaults.MaxTokens, 64)
		checkRange(where, "repeats", e.Repeats, 1, maxRepeats)
		checkRange(where, "max_tokens", e.MaxTokens, 1, maxMaxTokens)

		errs = append(errs, validatePlugin(where, e)...)
		errs = append(errs, validateProvider(where, e.Provider)...)

		for _, r := range routers {
			t, err := Resolve(r)
			if err != nil {
				addf("%s: %w", where, err)
				continue
			}
			cells = append(cells, Cell{
				ID:         e.ID + "/" + string(r),
				Experiment: e,
				Target:     t,
				Prompts:    seq,
				Repeats:    repeats,
				MaxTokens:  maxTokens,
				Timeout:    pick(nil, ef.Defaults.TimeoutSeconds, 90),
				HeadToHead: e.Router == RouterBoth,
			})
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cells, nil
}

func pick(own, def *int, fallback int) int {
	if own != nil {
		return *own
	}
	if def != nil {
		return *def
	}
	return fallback
}

func validatePlugin(where string, e Experiment) []error {
	var errs []error
	addf := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	p := e.Plugin
	both := p != nil && p.CostTier != nil && p.CostQualityTradeoff != nil
	if both && !e.AllowCostConflict {
		addf("%s: cost_tier and cost_quality_tradeoff are both set; cost_tier wins, so set allow_cost_conflict: true if this is the precedence experiment", where)
	}
	if e.AllowCostConflict && !both {
		addf("%s: allow_cost_conflict is set but the cell does not send both cost settings", where)
	}
	if p == nil {
		return errs
	}
	// An empty block would be dropped from the payload, which is the kind of
	// config-that-does-nothing this project is meant to catch.
	if len(p.AllowedModels) == 0 && len(p.ExcludedModels) == 0 && p.CostTier == nil && p.CostQualityTradeoff == nil {
		addf("%s: plugin block is present but sets nothing; remove it or set a field", where)
	}
	if p.CostTier != nil && !slices.Contains(CostTiers, *p.CostTier) {
		addf("%s: cost_tier must be one of %v, got %q", where, CostTiers, *p.CostTier)
	}
	if v := p.CostQualityTradeoff; v != nil && (*v < CostQualityTradeoffMin || *v > CostQualityTradeoffMax) {
		addf("%s: cost_quality_tradeoff must be %d..%d, got %d", where, CostQualityTradeoffMin, CostQualityTradeoffMax, *v)
	}
	for name, list := range map[string][]string{"allowed_models": p.AllowedModels, "excluded_models": p.ExcludedModels} {
		if len(list) > maxPatterns {
			addf("%s: %s has %d patterns, limit is %d", where, name, len(list), maxPatterns)
		}
		for _, pat := range list {
			if pat == "" || len(pat) > maxPattern {
				addf("%s: %s pattern %q must be 1..%d characters", where, name, pat, maxPattern)
			}
		}
	}
	return errs
}

func validateProvider(where string, p *Provider) []error {
	if p == nil {
		return nil
	}
	var errs []error
	addf := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if p.Sort != nil && !slices.Contains([]string{"price", "throughput", "latency"}, *p.Sort) {
		addf("%s: provider.sort must be price, throughput or latency, got %q", where, *p.Sort)
	}
	if p.DataCollection != nil && *p.DataCollection != "allow" && *p.DataCollection != "deny" {
		addf("%s: provider.data_collection must be allow or deny, got %q", where, *p.DataCollection)
	}
	for _, slug := range p.Only {
		if slices.Contains(p.Ignore, slug) {
			addf("%s: provider %q is in both only and ignore", where, slug)
		}
	}
	for name, list := range map[string][]string{"order": p.Order, "only": p.Only, "ignore": p.Ignore} {
		if slices.Contains(list, "") {
			addf("%s: provider.%s contains an empty slug", where, name)
		}
	}
	errs = append(errs, validateMaxPrice(where, p.MaxPrice)...)
	return errs
}

func validateMaxPrice(where string, m *MaxPrice) []error {
	if m == nil {
		return nil
	}
	var errs []error
	if m.Prompt == nil && m.Completion == nil && m.Request == nil && m.Image == nil {
		errs = append(errs, fmt.Errorf("%s: max_price is present but sets nothing", where))
	}
	for name, v := range map[string]*float64{"prompt": m.Prompt, "completion": m.Completion, "request": m.Request, "image": m.Image} {
		if v != nil && *v < 0 {
			errs = append(errs, fmt.Errorf("%s: max_price.%s must not be negative", where, name))
		}
	}
	return errs
}
