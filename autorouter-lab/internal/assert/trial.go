package assert

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// Status is the outcome of one check.
type Status string

const (
	// Pass: the config demonstrably took effect, or the invariant held.
	Pass Status = "pass"
	// Fail: the response contradicts the config that was sent.
	Fail Status = "fail"
	// Skip: the check does not apply to this trial.
	Skip Status = "skip"
	// Flag: nothing is wrong with the config, but the trial is not a clean
	// routing observation and analysis should set it aside.
	Flag Status = "flag"
)

// Result is one check's outcome, recorded as structured fields.
type Result struct {
	Check  string `json:"check"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Trial is the part of a trial record the checks read. Its JSON names are the
// record's field names, so a JSONL line decodes straight into it.
type Trial struct {
	RunID        string         `json:"run_id"`
	TrialID      string         `json:"trial_id"`
	ExperimentID string         `json:"experiment_id"`
	CellID       string         `json:"cell_id"`
	Repeat       int            `json:"repeat"`
	Step         int            `json:"step"`
	HeadToHead   bool           `json:"head_to_head"`
	Transport    string         `json:"transport"`
	Router       string         `json:"router"`
	Stickiness   string         `json:"stickiness"`
	SessionID    *string        `json:"session_id"`
	PromptID     string         `json:"prompt_id"`
	ExpectedTask string         `json:"expected_task_type"`
	ExpectStatus int            `json:"expect_status"`
	Plugin       *config.Plugin `json:"plugin_config"`
	// ProviderConfig is the provider block the cell sent, if any.
	ProviderConfig *config.Provider `json:"provider_config"`
	Status         int              `json:"status"`
	// Model answered the request; ResolvedTo is what the router chose. They
	// are different slugs for the same model in the normal case (short and
	// dated) and different models when ModelFallback is set.
	Model         string `json:"model"`
	ResolvedTo    string `json:"resolved_to"`
	ModelFallback bool   `json:"model_fallback"`
	// UpstreamAttempts is how many provider calls OpenRouter reported.
	UpstreamAttempts int     `json:"upstream_attempts"`
	Provider         string  `json:"provider"`
	TaskType         *string `json:"task_type"`
	GenerationID     string  `json:"generation_id"`
	CostUSD          float64 `json:"cost_usd"`
	CostAssumed      bool    `json:"cost_assumed"`
	LatencyMS        int64   `json:"latency_ms"`
	DryRun           bool    `json:"dry_run"`
	// Payload is the rendered request as logged: prompt text may be elided,
	// routing fields never are.
	Payload json.RawMessage `json:"request_payload"`
}

// Decision returns the slug that identifies the router's choice: ResolvedTo
// when the response reported one, otherwise the model that answered.
func (t Trial) Decision() string {
	if t.ResolvedTo != "" {
		return t.ResolvedTo
	}
	return t.Model
}

func (t Trial) ok() bool { return t.Status >= 200 && t.Status < 300 }

// decisionSlugs are the names a pattern may legitimately match for the
// router's choice. Patterns are written against short public slugs
// ("openai/gpt-5.1") while resolved_to is dated ("openai/gpt-5.1-20251113"),
// so both forms count, but the answering model only counts when it is the
// model the router chose.
func (t Trial) decisionSlugs() []string {
	var out []string
	if t.ResolvedTo != "" {
		out = append(out, t.ResolvedTo)
	}
	if t.Model != "" && !t.ModelFallback {
		out = append(out, t.Model)
	}
	return out
}

// CheckTrial runs every per-trial check. It needs nothing but the trial, so
// it runs both when a trial is recorded and again when a log is analysed.
func CheckTrial(t Trial) []Result {
	return []Result{
		checkPluginID(t),
		checkStatus(t),
		checkAllowed(t),
		checkExcluded(t),
		checkClassified(t),
		checkFallback(t),
	}
}

// checkPluginID re-derives the pairing from the logged request rather than
// from the plugin_id field, so it tests the bytes that were sent.
func checkPluginID(t Trial) Result {
	const name = "plugin_id_matches_router"
	var doc struct {
		Model   string `json:"model"`
		Plugins []struct {
			ID string `json:"id"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(t.Payload, &doc); err != nil {
		return Result{name, Skip, "request payload not available"}
	}
	want, isRouter := config.PluginIDForSlug(doc.Model)
	checked := false
	for _, p := range doc.Plugins {
		if !config.IsRouterPluginID(p.ID) {
			continue
		}
		checked = true
		if !isRouter || p.ID != want {
			return Result{name, Fail, fmt.Sprintf("model %q sent with plugin %q; the config was silently ignored", doc.Model, p.ID)}
		}
	}
	if !checked {
		return Result{name, Skip, "no router plugin block sent"}
	}
	return Result{name, Pass, ""}
}

// checkStatus compares the HTTP status with what the experiment declared. A
// request that is meant to fail and does is a pass.
func checkStatus(t Trial) Result {
	const name = "expected_status"
	want := t.ExpectStatus
	if want == 0 {
		want = http.StatusOK
	}
	if t.Status == want {
		return Result{name, Pass, ""}
	}
	return Result{name, Fail, fmt.Sprintf("status %d, expected %d", t.Status, want)}
}

func checkAllowed(t Trial) Result {
	const name = "allowed_models"
	if t.Plugin == nil || len(t.Plugin.AllowedModels) == 0 {
		return Result{name, Skip, "no allow-list"}
	}
	if !t.ok() {
		return Result{name, Skip, "no model was selected"}
	}
	for _, slug := range t.decisionSlugs() {
		if p, ok := MatchAny(t.Plugin.AllowedModels, slug); ok {
			return Result{name, Pass, fmt.Sprintf("%s matches %s", slug, p)}
		}
	}
	return Result{name, Fail, fmt.Sprintf("%s matches none of %v", t.Decision(), t.Plugin.AllowedModels)}
}

// checkExcluded is strict where checkAllowed is lenient: a match on any form
// of the chosen model's name, or on a fallback model that answered, fails.
func checkExcluded(t Trial) Result {
	const name = "excluded_models"
	if t.Plugin == nil || len(t.Plugin.ExcludedModels) == 0 {
		return Result{name, Skip, "no exclusions"}
	}
	if !t.ok() {
		return Result{name, Skip, "no model was selected"}
	}
	for _, slug := range []string{t.ResolvedTo, t.Model} {
		if slug == "" {
			continue
		}
		if p, ok := MatchAny(t.Plugin.ExcludedModels, slug); ok {
			return Result{name, Fail, fmt.Sprintf("%s matches excluded pattern %s", slug, p)}
		}
	}
	return Result{name, Pass, ""}
}

// checkClassified flags a success with no task type. The docs describe a
// missing task type as the sign that classification was unavailable and the
// router fell back to a default model set, so such a trial says nothing about
// normal routing.
func checkClassified(t Trial) Result {
	const name = "classified"
	if !t.ok() {
		return Result{name, Skip, "no successful response"}
	}
	if t.TaskType == nil || *t.TaskType == "" {
		return Result{name, Flag, "no task_type: classification unavailable, exclude from routing analysis"}
	}
	return Result{name, Pass, ""}
}

// checkFallback flags a trial answered by a fallback model: the router's
// choice failed upstream, so Model is not the routing decision.
func checkFallback(t Trial) Result {
	const name = "served_by_chosen_model"
	if !t.ok() {
		return Result{name, Skip, "no successful response"}
	}
	if t.ModelFallback {
		return Result{name, Flag, fmt.Sprintf("router chose %s, %s answered; use resolved_to", t.ResolvedTo, t.Model)}
	}
	return Result{name, Pass, ""}
}

// Clean reports whether a trial is a usable routing observation: it
// succeeded and no check failed or flagged it.
func Clean(t Trial) bool {
	if !t.ok() || t.DryRun {
		return false
	}
	for _, r := range CheckTrial(t) {
		if r.Status == Fail || r.Status == Flag {
			return false
		}
	}
	return true
}
