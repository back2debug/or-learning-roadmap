package assert

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

func ptr[T any](v T) *T { return &v }

func payload(model, pluginID string) json.RawMessage {
	if pluginID == "" {
		return json.RawMessage(`{"model":"` + model + `"}`)
	}
	return json.RawMessage(`{"model":"` + model + `","plugins":[{"id":"` + pluginID + `"}]}`)
}

// base is a clean, successful, classified trial.
func base() Trial {
	return Trial{
		RunID: "r", CellID: "c/auto", Router: "auto", Transport: "chat_completions", Stickiness: "isolate", PromptID: "p",
		Status: 200, Model: "google/gemini-3.8-flash", ResolvedTo: "google/gemini-3.8-flash-20260902",
		TaskType: ptr("math"), CostUSD: 0.001, Payload: payload("openrouter/auto", "auto-router"),
	}
}

func TestCheckTrial(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Trial)
		check  string
		want   Status
	}{
		{"matched pair", func(*Trial) {}, "plugin_id_matches_router", Pass},
		{"beta matched", func(t *Trial) { t.Payload = payload("openrouter/auto-beta", "auto-beta-router") }, "plugin_id_matches_router", Pass},
		{"stable slug with beta id", func(t *Trial) { t.Payload = payload("openrouter/auto", "auto-beta-router") }, "plugin_id_matches_router", Fail},
		{"beta slug with stable id", func(t *Trial) { t.Payload = payload("openrouter/auto-beta", "auto-router") }, "plugin_id_matches_router", Fail},
		{"router plugin on a pinned model", func(t *Trial) { t.Payload = payload("openai/gpt-5", "auto-router") }, "plugin_id_matches_router", Fail},
		{"no plugin block", func(t *Trial) { t.Payload = payload("openrouter/auto", "") }, "plugin_id_matches_router", Skip},
		{"unrelated plugin", func(t *Trial) { t.Payload = payload("openrouter/auto", "web") }, "plugin_id_matches_router", Skip},
		{"payload missing", func(t *Trial) { t.Payload = nil }, "plugin_id_matches_router", Skip},

		{"200 by default", func(*Trial) {}, "expected_status", Pass},
		{"unexpected 404", func(t *Trial) { t.Status = 404 }, "expected_status", Fail},
		{"expected 404", func(t *Trial) { t.Status, t.ExpectStatus = 404, 404 }, "expected_status", Pass},
		{"expected 404 but routed", func(t *Trial) { t.ExpectStatus = 404 }, "expected_status", Fail},
		{"502", func(t *Trial) { t.Status = 502 }, "expected_status", Fail},

		{"no allow-list", func(*Trial) {}, "allowed_models", Skip},
		{"allowed by vendor", func(t *Trial) { t.Plugin = &config.Plugin{AllowedModels: []string{"openai/*", "google/*"}} }, "allowed_models", Pass},
		{"not allowed", func(t *Trial) { t.Plugin = &config.Plugin{AllowedModels: []string{"anthropic/*"}} }, "allowed_models", Fail},
		{"exact short slug allows the dated decision", func(t *Trial) { t.Plugin = &config.Plugin{AllowedModels: []string{"google/gemini-3.8-flash"}} }, "allowed_models", Pass},
		{"allow-list on a 404", func(t *Trial) {
			t.Status, t.Plugin = 404, &config.Plugin{AllowedModels: []string{"x/*"}}
		}, "allowed_models", Skip},
		{"fallback model does not vouch for the decision", func(t *Trial) {
			t.Plugin = &config.Plugin{AllowedModels: []string{"google/*"}}
			t.ResolvedTo, t.Model, t.ModelFallback = "anthropic/claude-opus-5-20260723", "google/gemini-3.8-flash", true
		}, "allowed_models", Fail},

		{"no exclusions", func(*Trial) {}, "excluded_models", Skip},
		{"exclusion respected", func(t *Trial) { t.Plugin = &config.Plugin{ExcludedModels: []string{"*pro*"}} }, "excluded_models", Pass},
		{"excluded model chosen", func(t *Trial) { t.Plugin = &config.Plugin{ExcludedModels: []string{"*flash*"}} }, "excluded_models", Fail},
		{"excluded even though allowed", func(t *Trial) {
			t.Plugin = &config.Plugin{AllowedModels: []string{"google/*"}, ExcludedModels: []string{"google/gemini-3.8-flash"}}
		}, "excluded_models", Fail},
		{"excluded model answered as a fallback", func(t *Trial) {
			t.Plugin = &config.Plugin{ExcludedModels: []string{"*flash*"}}
			t.ResolvedTo, t.ModelFallback = "google/gemini-3.1-pro-20260219", true
		}, "excluded_models", Fail},

		{"classified", func(*Trial) {}, "classified", Pass},
		{"no task type", func(t *Trial) { t.TaskType = nil }, "classified", Flag},
		{"empty task type", func(t *Trial) { t.TaskType = ptr("") }, "classified", Flag},
		{"classification on a failure", func(t *Trial) { t.Status, t.TaskType = 404, nil }, "classified", Skip},

		{"chosen model answered", func(*Trial) {}, "served_by_chosen_model", Pass},
		{"fallback answered", func(t *Trial) { t.ModelFallback = true }, "served_by_chosen_model", Flag},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := base()
			tc.mutate(&tr)
			for _, r := range CheckTrial(tr) {
				if r.Check == tc.check {
					if r.Status != tc.want {
						t.Errorf("%s = %s (%s), want %s", tc.check, r.Status, r.Detail, tc.want)
					}
					return
				}
			}
			t.Fatalf("check %q not run", tc.check)
		})
	}
}

func TestClean(t *testing.T) {
	if !Clean(base()) {
		t.Error("a clean trial was rejected")
	}
	for name, mutate := range map[string]func(*Trial){
		"failed status": func(t *Trial) { t.Status = 404 },
		"fallback":      func(t *Trial) { t.ModelFallback = true },
		"unclassified":  func(t *Trial) { t.TaskType = nil },
		"dry run":       func(t *Trial) { t.DryRun = true },
		"mismatch":      func(t *Trial) { t.Payload = payload("openrouter/auto", "auto-beta-router") },
	} {
		tr := base()
		mutate(&tr)
		if Clean(tr) {
			t.Errorf("%s: trial counted as clean", name)
		}
	}
}

// A record line must decode into Trial with the names the runner writes.
func TestTrialDecodesFromRecord(t *testing.T) {
	line := `{"msg":"trial","run_id":"r1","cell_id":"tier-low/auto","router":"auto","transport":"chat_completions","stickiness":"isolate",
	"prompt_id":"p","expect_status":404,"plugin_config":{"AllowedModels":["google/*"],"ExcludedModels":null,"CostTier":"low","CostQualityTradeoff":0},
	"status":200,"model":"a/b","resolved_to":"a/b-1","model_fallback":true,"task_type":"math","cost_usd":0.002,
	"request_payload":{"model":"openrouter/auto","plugins":[{"id":"auto-router"}]}}`
	var tr Trial
	if err := json.Unmarshal([]byte(line), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.CellID != "tier-low/auto" || tr.ExpectStatus != 404 || !tr.ModelFallback || tr.CostUSD != 0.002 ||
		tr.TaskType == nil || *tr.TaskType != "math" || tr.Plugin == nil || tr.Plugin.CostTier == nil || *tr.Plugin.CostTier != "low" ||
		tr.Plugin.CostQualityTradeoff == nil || *tr.Plugin.CostQualityTradeoff != 0 || len(tr.Plugin.AllowedModels) != 1 {
		t.Errorf("decoded trial = %+v plugin %+v", tr, tr.Plugin)
	}
	if r := checkPluginID(tr); r.Status != Pass {
		t.Errorf("plugin id check on decoded payload = %s", r.Status)
	}
}

// cell builds n clean trials for one cell that all chose model at cost.
func cell(id string, plugin *config.Plugin, model string, cost float64) []Trial {
	var out []Trial
	for i := range 3 {
		tr := base()
		tr.CellID, tr.Repeat, tr.Plugin, tr.CostUSD = id, i, plugin, cost
		tr.ResolvedTo, tr.Model = model+"-2026", model
		out = append(out, tr)
	}
	return out
}

func tier(s string) *config.Plugin { return &config.Plugin{CostTier: ptr(config.CostTier(s))} }
func dial(d int) *config.Plugin    { return &config.Plugin{CostQualityTradeoff: &d} }
func both(s string, d int) *config.Plugin {
	return &config.Plugin{CostTier: ptr(config.CostTier(s)), CostQualityTradeoff: &d}
}

func findings(t *testing.T, trials []Trial, check string) map[string]Finding {
	t.Helper()
	out := map[string]Finding{}
	for _, f := range CheckRun(trials) {
		if f.Check == check {
			out[f.Cells[0]] = f
		}
	}
	return out
}

func join(groups ...[]Trial) []Trial {
	var out []Trial
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func TestObservableEffect(t *testing.T) {
	trials := join(
		cell("baseline", nil, "v/cheap", 0.001),
		cell("tier-low", tier("low"), "v/cheap", 0.001),
		cell("tier-max", tier("max"), "v/dear", 0.02),
	)
	got := findings(t, trials, "observable_effect")
	if got["tier-low"].Status != Flag {
		t.Errorf("tier-low = %s, want flag: it chose what the baseline chose", got["tier-low"].Status)
	}
	if got["tier-max"].Status != Pass {
		t.Errorf("tier-max = %s, want pass", got["tier-max"].Status)
	}
	if _, ok := got["baseline"]; ok {
		t.Error("the baseline was compared with itself")
	}
	// Without a baseline there is nothing to compare against.
	if n := len(findings(t, cell("tier-max", tier("max"), "v/dear", 0.02), "observable_effect")); n != 0 {
		t.Errorf("%d findings without a baseline", n)
	}
}

func TestTierDirection(t *testing.T) {
	trials := join(
		cell("low", tier("low"), "v/a", 0.001),
		cell("medium", tier("medium"), "v/a", 0.001), // same model: band did not visibly move
		cell("high", tier("high"), "v/c", 0.010),
		cell("xhigh", tier("xhigh"), "v/d", 0.009), // different model, slightly cheaper: inconclusive
		cell("max", tier("max"), "v/e", 0.004),     // under half the band below: inverted
		cell("dial", dial(5), "v/z", 9),            // not part of the sweep
	)
	got := findings(t, trials, "cost_tier_direction")
	for id, want := range map[string]Status{"medium": Flag, "high": Pass, "xhigh": Flag, "max": Fail} {
		if got[id].Status != want {
			t.Errorf("%s = %s (%s), want %s", id, got[id].Status, got[id].Detail, want)
		}
	}
	if len(got) != 4 {
		t.Errorf("%d findings, want 4", len(got))
	}
}

func TestPrecedence(t *testing.T) {
	sweep := join(
		cell("tier-max", tier("max"), "v/dear", 0.02),
		cell("tier-low", tier("low"), "v/cheap", 0.001),
		cell("dial-10", dial(10), "v/cheapest", 0.0005),
		cell("dial-0", dial(0), "v/pricey", 0.01),
	)
	tests := []struct {
		name  string
		cell  []Trial
		want  Status
		words string
	}{
		{"tier wins", cell("x", both("max", 10), "v/dear", 0.02), Pass, "cost_tier wins"},
		{"dial wins", cell("x", both("max", 10), "v/cheapest", 0.0005), Fail, "cost_quality_tradeoff wins"},
		{"neither", cell("x", both("max", 10), "v/other", 0.003), Flag, "matches neither"},
		{"nothing to compare with", cell("x", both("high", 4), "v/dear", 0.02), Skip, "needs a tier-only cell"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := findings(t, join(sweep, tc.cell), "cost_tier_precedence")["x"]
			if f.Status != tc.want || !strings.Contains(f.Detail, tc.words) {
				t.Errorf("got %s (%s), want %s containing %q", f.Status, f.Detail, tc.want, tc.words)
			}
		})
	}
	// When tier-only and dial-only agree, the pair cannot separate them.
	same := join(cell("tier-low", tier("low"), "v/a", 1), cell("dial-9", dial(9), "v/a", 1), cell("x", both("low", 9), "v/a", 1))
	if f := findings(t, same, "cost_tier_precedence")["x"]; f.Status != Flag {
		t.Errorf("indistinguishable pair = %s, want flag", f.Status)
	}
}

// A price cap on one side must not be read as the effect of a cost setting:
// capped and uncapped cells are compared only with their own kind.
func TestProviderBlockSeparatesGroups(t *testing.T) {
	capped := &config.Provider{MaxPrice: &config.MaxPrice{Completion: ptr(125.0)}}
	withCap := func(ts []Trial) []Trial {
		for i := range ts {
			ts[i].ProviderConfig = capped
		}
		return ts
	}
	trials := join(
		cell("nocap-base", nil, "v/a", 1),
		withCap(cell("cap-base", nil, "v/b", 1)),
		withCap(cell("cap-low", tier("low"), "v/b", 1)),
		withCap(cell("cap-max", tier("max"), "v/d", 3)),
		cell("nocap-high", tier("high"), "v/c", 2),
	)
	eff := findings(t, trials, "observable_effect")
	if eff["cap-low"].Status != Flag || eff["cap-low"].Cells[1] != "cap-base" {
		t.Errorf("cap-low = %+v, want a flag against cap-base", eff["cap-low"])
	}
	if eff["nocap-high"].Status != Pass || eff["nocap-high"].Cells[1] != "nocap-base" {
		t.Errorf("nocap-high = %+v, want a pass against nocap-base", eff["nocap-high"])
	}
	dir := findings(t, trials, "cost_tier_direction")
	if len(dir) != 1 || dir["cap-max"].Cells[1] != "cap-low" {
		t.Errorf("tier direction = %v, want only cap-max compared with cap-low", dir)
	}
}

// Comparisons must not mix routers, transports or prompts, and must ignore
// trials that are not clean, independent routing decisions.
func TestCellsScope(t *testing.T) {
	alter := func(ts []Trial, f func(*Trial)) []Trial {
		for i := range ts {
			f(&ts[i])
		}
		return ts
	}
	trials := join(
		cell("baseline/auto", nil, "v/a", 1),
		alter(cell("baseline/beta", nil, "v/b", 1), func(t *Trial) { t.Router = "auto-beta" }),
		alter(cell("tier/beta", tier("max"), "v/a", 1), func(t *Trial) { t.Router = "auto-beta" }),
		alter(cell("sticky", tier("max"), "v/a", 1), func(t *Trial) { t.Stickiness = "sticky" }),
		alter(cell("fell-back", tier("high"), "v/a", 1), func(t *Trial) { t.ModelFallback = true }),
	)
	got := findings(t, trials, "observable_effect")
	if len(got) != 1 {
		t.Fatalf("findings = %v, want only tier/beta", fmt.Sprint(got))
	}
	// tier/beta chose v/a, which is the stable baseline's choice, not beta's.
	if f := got["tier/beta"]; f.Status != Pass || !strings.Contains(f.Group, "auto-beta") {
		t.Errorf("tier/beta = %s in %q; it must be compared with the beta baseline only", f.Status, f.Group)
	}
}
