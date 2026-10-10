package assert

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, slug string
		want          bool
	}{
		// The documented examples.
		{"anthropic/*", "anthropic/claude-sonnet-4.5", true},
		{"anthropic/*", "openai/gpt-5.1", false},
		{"openai/gpt-5*", "openai/gpt-5", true},
		{"openai/gpt-5*", "openai/gpt-5.1", true},
		{"openai/gpt-5*", "openai/gpt-5-mini", true},
		{"openai/gpt-5*", "openai/gpt-4o", false},
		{"openai/gpt-5.1", "openai/gpt-5.1", true},
		{"openai/gpt-5.1", "openai/gpt-5.1-20251113", false},
		{"*/claude-*", "anthropic/claude-opus-4", true},
		{"*/claude-*", "amazon/claude-haiku", true},
		{"*/claude-*", "anthropic/opus", false},
		{"google/*", "google/gemini-3.8-flash-20260902", true},
		// Wildcard placement.
		{"*", "anything/at-all", true},
		{"*", "", true},
		{"*flash*", "google/gemini-3.8-flash", true},
		{"*flash*", "deepseek/deepseek-v4-flash-0731", true},
		{"*flash*", "google/gemini-3.8-pro", false},
		{"*-flash", "google/gemini-3.8-flash", true},
		{"*-flash", "google/gemini-3.8-flash-lite", false},
		{"a*b*c", "a-b-c", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
		{"a**c", "abc", true},
		// Anchoring: a literal pattern is a whole-slug match, and the ends of
		// a wildcard pattern may not overlap.
		{"openai/gpt-5", "openai/gpt-5.1", false},
		{"gpt-5", "openai/gpt-5", false},
		{"ab*ba", "aba", false},
		{"ab*ba", "abba", true},
		{"a*a", "a", false},
		// Case and empties.
		{"Anthropic/*", "anthropic/claude", false},
		{"", "anthropic/claude", false},
		{"", "", false},
		{"anthropic/*", "", false},
		// Regex metacharacters are literal.
		{"openai/gpt-5.1", "openai/gpt-5x1", false},
		{"a+b", "a+b", true},
		{"a?b", "axb", false},
		{"[a]*", "a/x", false},
	}
	for _, tc := range tests {
		if got := Match(tc.pattern, tc.slug); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.slug, got, tc.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	if p, ok := MatchAny([]string{"openai/*", "google/*"}, "google/gemini"); !ok || p != "google/*" {
		t.Errorf("MatchAny = %q, %v", p, ok)
	}
	if _, ok := MatchAny(nil, "google/gemini"); ok {
		t.Error("MatchAny matched with no patterns")
	}
}
