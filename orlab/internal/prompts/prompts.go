// Package prompts loads the suite of canonical PromptSpecs: an embedded
// default, overridable with --suite pointing at a JSON file of the same
// shape (an array of PromptSpec using Go field names).
package prompts

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/dialect"
)

//go:embed suite.json
var embeddedSuite []byte

// fillerSentence is repeated to synthesize long_ctx prompts. Benign,
// deterministic, cheap to tokenize.
const fillerSentence = "The quick brown fox jumps over the lazy dog near the riverbank at dawn. "

var (
	fillerPattern = regexp.MustCompile(`\{\{FILLER:(\d+)\}\}`)
	padPattern    = regexp.MustCompile(`\{\{PAD:(\d+)\}\}`)
)

// Load returns the suite at path, or the embedded default when path is "".
func Load(path string) ([]dialect.PromptSpec, error) {
	raw := embeddedSuite
	if path != "" {
		// The suite path is supplied by the operator via -suite; reading an
		// arbitrary local file is the documented purpose of that flag.
		b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied suite path
		if err != nil {
			return nil, fmt.Errorf("prompts: reading suite: %w", err)
		}
		raw = b
	}
	var suite []dialect.PromptSpec
	if err := json.Unmarshal(raw, &suite); err != nil {
		return nil, fmt.Errorf("prompts: decoding suite: %w", err)
	}
	if err := validate(suite); err != nil {
		return nil, err
	}
	for i := range suite {
		for j := range suite[i].Turns {
			suite[i].Turns[j].Text = expandFiller(suite[i].Turns[j].Text)
		}
		suite[i].SessionID = expandPad(suite[i].SessionID)
	}
	return suite, nil
}

func validate(suite []dialect.PromptSpec) error {
	if len(suite) == 0 {
		return fmt.Errorf("prompts: suite is empty")
	}
	seen := map[string]bool{}
	for _, s := range suite {
		switch {
		case s.ID == "":
			return fmt.Errorf("prompts: entry with empty ID")
		case seen[s.ID]:
			return fmt.Errorf("prompts: duplicate entry ID %q", s.ID)
		case len(s.Turns) == 0:
			return fmt.Errorf("prompts: entry %q has no turns", s.ID)
		case s.Kind == "":
			return fmt.Errorf("prompts: entry %q has no kind", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// expandPad replaces {{PAD:n}} with n "x" characters, so length-cap probes
// (e.g. a session_id of exactly 200 or 300 chars) are written by intent
// instead of by counting.
func expandPad(text string) string {
	return padPattern.ReplaceAllStringFunc(text, func(m string) string {
		n, err := strconv.Atoi(padPattern.FindStringSubmatch(m)[1])
		if err != nil || n < 0 || n > 10000 {
			return m
		}
		return strings.Repeat("x", n)
	})
}

// expandFiller replaces {{FILLER:n}} with n repetitions of the filler
// sentence, so the suite file stays readable while long_ctx entries carry a
// few thousand tokens.
func expandFiller(text string) string {
	return fillerPattern.ReplaceAllStringFunc(text, func(m string) string {
		n, err := strconv.Atoi(fillerPattern.FindStringSubmatch(m)[1])
		if err != nil || n < 0 || n > 20000 {
			return m
		}
		return strings.Repeat(fillerSentence, n)
	})
}
