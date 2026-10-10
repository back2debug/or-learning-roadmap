package config

import (
	"errors"
	"strings"
	"testing"
)

const testPrompts = `
version: 1
prompts:
  - id: p1
    expected_task_type: math
    text: what is 2+2
`

func expand(t *testing.T, experiments string) ([]Cell, error) {
	t.Helper()
	var pf PromptFile
	if err := decodeStrict(strings.NewReader(testPrompts), &pf); err != nil {
		t.Fatalf("prompts: %v", err)
	}
	var ef File
	if err := decodeStrict(strings.NewReader(experiments), &ef); err != nil {
		return nil, err
	}
	return Expand(ef, pf)
}

func exp(body string) string {
	return "version: 1\nexperiments:\n  - id: e1\n    transport: chat_completions\n    prompt_id: p1\n" + body
}

func TestExpandMatrix(t *testing.T) {
	cells, err := expand(t, `
version: 1
defaults: {repeats: 3, max_tokens: 32}
experiments:
  - id: a
    router: both
    transport: chat_completions
    prompt_id: p1
    plugin: {cost_tier: low}
  - id: b
    router: auto-beta
    transport: messages
    prompt_id: p1
    repeats: 5
    stickiness: sticky
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id         string
		pluginID   string
		repeats    int
		headToHead bool
		stickiness Stickiness
	}{
		{"a/auto", "auto-router", 3, true, StickinessIsolate},
		{"a/auto-beta", "auto-beta-router", 3, true, StickinessIsolate},
		{"b/auto-beta", "auto-beta-router", 5, false, StickinessSticky},
	}
	if len(cells) != len(want) {
		t.Fatalf("got %d cells, want %d", len(cells), len(want))
	}
	for i, w := range want {
		c := cells[i]
		if c.ID != w.id || c.Target.PluginID != w.pluginID || c.Repeats != w.repeats ||
			c.HeadToHead != w.headToHead || c.Experiment.Stickiness != w.stickiness || c.MaxTokens != 32 {
			t.Errorf("cell %d = %+v, want %+v", i, c, w)
		}
	}
}

// A zero dial must survive decoding as a non-nil pointer to 0, distinct from
// an absent dial.
func TestZeroDialIsNotUnset(t *testing.T) {
	cells, err := expand(t, exp("    router: auto\n    plugin: {cost_quality_tradeoff: 0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := cells[0].Experiment.Plugin.CostQualityTradeoff; v == nil || *v != 0 {
		t.Fatalf("cost_quality_tradeoff = %v, want pointer to 0", v)
	}
	cells, err = expand(t, exp("    router: auto\n    plugin: {cost_tier: low}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := cells[0].Experiment.Plugin.CostQualityTradeoff; v != nil {
		t.Fatalf("absent cost_quality_tradeoff decoded as %d, want nil", *v)
	}
}

func TestStrictDecodeAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring; empty means valid
	}{
		{"valid", exp("    router: auto\n"), ""},
		{"typo key", exp("    router: auto\n    plugin: {cost_teir: low}\n"), "cost_teir"},
		{"unknown top-level key", "version: 1\nexperimnets: []\n", "experimnets"},
		{"plugin id not configurable", exp("    router: auto\n    plugin: {id: auto-beta-router, cost_tier: low}\n"), "field id not found"},
		{"two documents", exp("    router: auto\n") + "---\nversion: 1\n", "more than one YAML document"},
		{"empty document", "", "empty"},
		{"bad router", exp("    router: stable\n"), "router must be"},
		{"missing router", exp(""), "router must be"},
		{"bad transport", "version: 1\nexperiments:\n  - {id: e1, router: auto, transport: responses, prompt_id: p1}\n", "transport must be"},
		{"unknown prompt", "version: 1\nexperiments:\n  - {id: e1, router: auto, transport: messages, prompt_id: nope}\n", "not in the prompt library"},
		{"prompt id and sequence", exp("    router: auto\n    stickiness: sticky\n    prompt_sequence: [p1]\n"), "not both"},
		{"sequence under isolate", "version: 1\nexperiments:\n  - {id: e1, router: auto, transport: chat_completions, prompt_sequence: [p1, p1]}\n", "prompt_sequence needs stickiness"},
		{"valid sequence", "version: 1\nexperiments:\n  - {id: e1, router: auto, transport: chat_completions, stickiness: sticky, prompt_sequence: [p1, p1]}\n", ""},
		{"no prompt at all", "version: 1\nexperiments:\n  - {id: e1, router: auto, transport: chat_completions}\n", "is required"},
		{"wait too long", exp("    router: auto\n    wait_before_seconds: 99999\n"), "wait_before_seconds must be"},
		{"bad default max price", "version: 1\ndefaults: {max_price: {completion: -2}}\nexperiments:\n  - {id: e1, router: auto, transport: chat_completions, prompt_id: p1}\n", "must not be negative"},
		{"duplicate id", exp("    router: auto\n") + "  - {id: e1, router: auto, transport: messages, prompt_id: p1}\n", "duplicate id"},
		{"bad cost tier", exp("    router: auto\n    plugin: {cost_tier: ultra}\n"), "cost_tier must be one of"},
		{"dial out of range", exp("    router: auto\n    plugin: {cost_quality_tradeoff: 11}\n"), "cost_quality_tradeoff must be"},
		{"both cost settings", exp("    router: auto\n    plugin: {cost_tier: low, cost_quality_tradeoff: 3}\n"), "allow_cost_conflict"},
		{"both cost settings allowed", exp("    router: auto\n    allow_cost_conflict: true\n    plugin: {cost_tier: low, cost_quality_tradeoff: 3}\n"), ""},
		{"allow flag with nothing to allow", exp("    router: auto\n    allow_cost_conflict: true\n    plugin: {cost_tier: low}\n"), "does not send both"},
		{"empty plugin block", exp("    router: auto\n    plugin: {}\n"), "sets nothing"},
		{"empty pattern", exp("    router: auto\n    plugin: {allowed_models: [\"\"]}\n"), "pattern"},
		{"head-to-head sticky", exp("    router: both\n    stickiness: sticky\n"), "requires stickiness isolate"},
		{"head-to-head implicit", exp("    router: both\n    stickiness: implicit\n"), "requires stickiness isolate"},
		{"single router sticky", exp("    router: auto\n    stickiness: sticky\n    session_id: abc\n"), ""},
		{"session id without sticky", exp("    router: auto\n    session_id: abc\n"), "only meaningful with stickiness sticky"},
		{"session id too long", exp("    router: auto\n    stickiness: sticky\n    session_id: " + strings.Repeat("x", 257) + "\n"), "session_id must be"},
		{"bad stickiness", exp("    router: auto\n    stickiness: pinned\n"), "stickiness must be"},
		{"repeats zero", exp("    router: auto\n    repeats: 0\n"), "repeats must be"},
		{"bad sort", exp("    router: auto\n    provider: {sort: cheapest}\n"), "provider.sort"},
		{"bad data collection", exp("    router: auto\n    provider: {data_collection: maybe}\n"), "data_collection"},
		{"only and ignore overlap", exp("    router: auto\n    provider: {only: [a], ignore: [a]}\n"), "both only and ignore"},
		{"negative max price", exp("    router: auto\n    provider: {max_price: {prompt: -1}}\n"), "must not be negative"},
		{"empty max price", exp("    router: auto\n    provider: {max_price: {}}\n"), "sets nothing"},
		{"valid provider", exp("    router: auto\n    provider: {max_price: {prompt: 0, completion: 2.5}, zdr: false, sort: price}\n"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := expand(t, tc.yaml)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// defaults.max_price reaches every cell that does not set its own, and a
// cell's own value wins.
func TestDefaultMaxPrice(t *testing.T) {
	cells, err := expand(t, `
version: 1
defaults: {max_price: {completion: 50}}
experiments:
  - {id: a, router: auto, transport: chat_completions, prompt_id: p1}
  - {id: b, router: auto, transport: chat_completions, prompt_id: p1, provider: {zdr: true}}
  - {id: c, router: auto, transport: chat_completions, prompt_id: p1, provider: {max_price: {completion: 5}}}
`)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{50, 50, 5} {
		p := cells[i].Experiment.Provider
		if p == nil || p.MaxPrice == nil || p.MaxPrice.Completion == nil || *p.MaxPrice.Completion != want {
			t.Errorf("cell %s: max_price.completion = %+v, want %v", cells[i].ID, p, want)
		}
	}
	if z := cells[1].Experiment.Provider.ZDR; z == nil || !*z {
		t.Error("cell b lost its own provider settings")
	}
}

// The shipped YAML files must load.
func TestShippedConfigLoads(t *testing.T) {
	for _, f := range []string{"experiments.yaml", "experiments-slow.yaml", "experiments-followup.yaml"} {
		cells, err := Load("../../"+f, "../../prompts.yaml")
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(cells) == 0 {
			t.Fatalf("%s: no cells", f)
		}
	}
}

func TestAPIKey(t *testing.T) {
	tests := []struct {
		name, value string
		set, ok     bool
	}{
		{"set", "sk-or-v1-abc", true, true},
		{"trimmed", "  sk-or-v1-abc\n", true, true},
		{"empty", "", true, false},
		{"blank", "   ", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(APIKeyEnv, tc.value)
			got, err := APIKey()
			if tc.ok {
				if err != nil || got != "sk-or-v1-abc" {
					t.Fatalf("APIKey() = %q, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrNoAPIKey) || !strings.Contains(err.Error(), "export "+APIKeyEnv) {
				t.Fatalf("error = %v, want ErrNoAPIKey naming the export", err)
			}
		})
	}
}
