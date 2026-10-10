// Package logging provides the JSONL trial sink, the console handler, and the
// redaction that both share.
package logging

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
)

// Redacted replaces any secret value.
const Redacted = "[REDACTED]"

// Body marks bytes that carry prompt or model text: a raw request or response
// body. It is logged only when Options.LogBodies is set.
type Body []byte

// Payload marks a rendered JSON request. It is always logged, but with prompt
// text elided unless Options.LogBodies is set, so the routing configuration is
// visible in every record without the prompt riding along.
type Payload []byte

// Options configures redaction.
type Options struct {
	// LogBodies enables prompt and response text in the logs.
	LogBodies bool
	// Secrets are literal values scrubbed wherever they appear, in addition
	// to the pattern-based rules. Pass the API key here.
	Secrets []string
}

var (
	// sensitiveKey matches attribute and header names whose values are
	// secrets by construction. "token" must end the word so that usage
	// counters such as prompt_tokens are left alone.
	sensitiveKey = regexp.MustCompile(`(?i)(^|[-_.])(authorization|auth|cookie|secret|password|passwd|credential|credentials|token|api[-_]?key|apikey)($|[-_.])`)

	secretValues = []*regexp.Regexp{
		regexp.MustCompile(`sk-or-[A-Za-z0-9_-]{8,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	}
)

type redactor struct {
	logBodies bool
	secrets   []string
}

// NewReplaceAttr returns a slog ReplaceAttr function that applies every
// redaction rule.
//
// Redaction lives here, in the handler, so it applies to every record on every
// sink and a caller cannot forget it. The wire-capture RoundTripper sees
// unredacted bytes by construction (it has to, in order to forward them), so
// this function is the boundary between capture and anything persistent.
func NewReplaceAttr(o Options) func([]string, slog.Attr) slog.Attr {
	r := &redactor{logBodies: o.LogBodies}
	for _, s := range o.Secrets {
		if s != "" {
			r.secrets = append(r.secrets, s)
		}
	}
	return r.replace
}

func (r *redactor) replace(_ []string, a slog.Attr) slog.Attr {
	if sensitiveKey.MatchString(a.Key) {
		a.Value = slog.StringValue(Redacted)
		return a
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(r.scrub(a.Value.String()))
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case Body:
			a.Value = r.body(v)
		case Payload:
			a.Value = r.payload(v)
		case json.RawMessage:
			a.Value = r.jsonOrString([]byte(v))
		case []byte:
			a.Value = slog.StringValue(r.scrub(string(v)))
		case error:
			a.Value = slog.StringValue(r.scrub(v.Error()))
		case nil:
		default:
			a.Value = r.other(v)
		}
	}
	return a
}

// other handles any value without a dedicated case. It is rendered here
// rather than by the handler, so a struct cannot carry a secret past the
// scrubber; string-like values stay plain strings so the console reads well.
func (r *redactor) other(v any) slog.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return slog.AnyValue(nil)
		}
		rv = rv.Elem()
	}
	if s, ok := v.(fmt.Stringer); ok {
		return slog.StringValue(r.scrub(s.String()))
	}
	if rv.Kind() == reflect.String {
		return slog.StringValue(r.scrub(rv.String()))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return slog.StringValue(r.scrub(fmt.Sprintf("%v", v)))
	}
	return r.jsonOrString(b)
}

// scrub removes known secret literals and secret-shaped substrings.
func (r *redactor) scrub(s string) string {
	for _, lit := range r.secrets {
		s = strings.ReplaceAll(s, lit, Redacted)
	}
	for _, re := range secretValues {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

func (r *redactor) jsonOrString(b []byte) slog.Value {
	s := r.scrub(string(b))
	if json.Valid([]byte(s)) {
		return slog.AnyValue(json.RawMessage(s))
	}
	return slog.StringValue(s)
}

func (r *redactor) body(b Body) slog.Value {
	if !r.logBodies {
		return slog.StringValue(elided(b))
	}
	return r.jsonOrString(b)
}

// payload keeps the request's shape and routing fields but, unless bodies are
// enabled, replaces prompt text with a size and hash. Re-encoding sorts object
// keys, so this is a rendering of the payload, not the wire bytes; the exact
// bytes are the Body logged under -log-bodies.
func (r *redactor) payload(p Payload) slog.Value {
	if r.logBodies {
		return r.jsonOrString(p)
	}
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		// Not a JSON object: nothing can be shown safely.
		return slog.StringValue(elided(p))
	}
	for _, k := range []string{"system", "prompt"} {
		if v, ok := doc[k]; ok {
			doc[k] = elideValue(v)
		}
	}
	if msgs, ok := doc["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if c, ok := mm["content"]; ok {
					mm["content"] = elideValue(c)
				}
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return slog.StringValue(elided(p))
	}
	return r.jsonOrString(out)
}

func elideValue(v any) string {
	if s, ok := v.(string); ok {
		return elided([]byte(s))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[elided]"
	}
	return elided(b)
}

// elided describes omitted bytes. The hash prefix lets identical prompts be
// matched across records without storing them.
func elided(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("[elided %d bytes sha256:%x; pass -log-bodies to record]", len(b), sum[:4])
}

// KeyLast4 returns the only form of the API key that may ever be logged, and
// only when the operator passes -debug-key-fingerprint.
func KeyLast4(key string) string {
	if len(key) < 12 {
		return "[key too short to fingerprint]"
	}
	return "..." + key[len(key)-4:]
}
