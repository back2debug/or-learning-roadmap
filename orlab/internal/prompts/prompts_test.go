package prompts

import (
	"strings"
	"testing"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/dialect"
)

func TestEmbeddedSuiteLoads(t *testing.T) {
	suite, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]dialect.PromptSpec{}
	for _, s := range suite {
		byID[s.ID] = s
	}

	for _, want := range []string{
		"plain", "system", "multiturn", "prefill", "json_schema", "tools",
		"reasoning", "stop_seq", "param_fate", "param_fate_strict", "unicode",
		"long_ctx", "buffered", "extras_parity", "err_bad_model",
		"err_missing_max_tokens", "err_bad_tool_schema", "err_session_overflow",
	} {
		if _, ok := byID[want]; !ok {
			t.Errorf("suite missing entry %q", want)
		}
	}

	if got := len(byID["extras_parity"].SessionID); got != 200 {
		t.Errorf("extras_parity session_id length = %d, want 200", got)
	}
	if got := len(byID["err_session_overflow"].SessionID); got != 300 {
		t.Errorf("err_session_overflow session_id length = %d, want 300", got)
	}
	if long := byID["long_ctx"].Turns[0].Text; len(long) < 10000 || strings.Contains(long, "{{FILLER") {
		t.Errorf("long_ctx filler not expanded (len %d)", len(long))
	}
	if byID["err_bad_model"].Model != "orlab/does-not-exist" {
		t.Errorf("err_bad_model should pin an invalid slug")
	}
	if byID["param_fate"].TopK == nil || *byID["param_fate"].TopK != 40 {
		t.Error("param_fate must send top_k")
	}
	if byID["param_fate_strict"].RequireParameters == nil || !*byID["param_fate_strict"].RequireParameters {
		t.Error("param_fate_strict must set provider.require_parameters")
	}
	if byID["err_missing_max_tokens"].MaxTokens != 0 {
		t.Error("err_missing_max_tokens must omit max_tokens")
	}

	// Every entry must build cleanly on every dialect.
	for _, s := range suite {
		for _, d := range []dialect.Dialect{dialect.Chat{}, dialect.Messages{}, dialect.Responses{}} {
			if s.Model == "" {
				s.Model = "test/model"
			}
			if _, _, err := d.BuildRequest(s); err != nil {
				t.Errorf("entry %q fails to build on %s: %v", s.ID, d.Name(), err)
			}
		}
	}
}

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	if err := validate([]dialect.PromptSpec{
		{ID: "a", Kind: "plain", Turns: []dialect.Turn{{Role: "user", Text: "x"}}},
		{ID: "a", Kind: "plain", Turns: []dialect.Turn{{Role: "user", Text: "x"}}},
	}); err == nil {
		t.Error("duplicate IDs must be rejected")
	}
}
