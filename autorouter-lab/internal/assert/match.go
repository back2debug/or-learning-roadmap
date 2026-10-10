// Package assert checks, per trial, whether the configuration that was sent
// took observable effect in the response.
package assert

import "strings"

// Match reports whether a model slug matches an allowed_models or
// excluded_models pattern.
//
// The documented syntax is a single wildcard: "*" matches any part of the
// name. The docs' examples ("anthropic/*", "openai/gpt-5*", "*/claude-*") do
// not say whether "*" may span the "/" between vendor and model; this
// implementation lets it, which is the reading under which every documented
// example works. Matching is case-sensitive, as slugs are lowercase.
func Match(pattern, slug string) bool {
	if pattern == "" {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == slug
	}
	// The first and last literal parts are anchored; the rest must appear in
	// order in between.
	first, last := parts[0], parts[len(parts)-1]
	if !strings.HasPrefix(slug, first) {
		return false
	}
	slug = slug[len(first):]
	if len(slug) < len(last) || !strings.HasSuffix(slug, last) {
		return false
	}
	slug = slug[:len(slug)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(slug, mid)
		if i < 0 {
			return false
		}
		slug = slug[i+len(mid):]
	}
	return true
}

// MatchAny reports whether slug matches any of the patterns, and which one.
func MatchAny(patterns []string, slug string) (string, bool) {
	for _, p := range patterns {
		if Match(p, slug) {
			return p, true
		}
	}
	return "", false
}
