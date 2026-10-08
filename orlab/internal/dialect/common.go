package dialect

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// providerPrefs is OpenRouter's cross-cutting provider routing object. Only
// the fields this harness exercises are modeled.
type providerPrefs struct {
	RequireParameters *bool `json:"require_parameters,omitempty"`
}

// debugOptions is OpenRouter's request-transformation debug knob
// (streaming-only per docs; sending it anyway on /messages is a probe).
type debugOptions struct {
	EchoUpstreamBody bool `json:"echo_upstream_body"`
}

func providerFrom(spec PromptSpec) *providerPrefs {
	if spec.RequireParameters == nil {
		return nil
	}
	return &providerPrefs{RequireParameters: spec.RequireParameters}
}

func debugFrom(spec PromptSpec) *debugOptions {
	if !spec.EchoUpstream {
		return nil
	}
	return &debugOptions{EchoUpstreamBody: true}
}

// upstreamBodyKeys are the candidate field names for the echoed upstream
// request inside a debug payload. The exact name is undocumented; we try the
// plausible ones and fall back to keeping the whole debug object.
var upstreamBodyKeys = []string{"echo_upstream_body", "upstream_body", "body", "request"}

func extractUpstreamBody(debug json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(debug, &m); err == nil {
		for _, k := range upstreamBodyKeys {
			if v, ok := m[k]; ok && !bytes.Equal(v, []byte("null")) {
				return v
			}
		}
	}
	return debug
}

// marshalBody encodes a request struct deterministically (struct fields keep
// declaration order; map keys are sorted by encoding/json).
func marshalBody(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("dialect: encoding request body: %w", err)
	}
	return b, nil
}

func newNotes() BuildNotes {
	return BuildNotes{
		Renamed:       map[string]string{},
		Transformed:   map[string]string{},
		Unexpressible: map[string]string{},
	}
}

func rawCopy(b []byte) json.RawMessage {
	return json.RawMessage(bytes.Clone(b))
}

// safeRaw copies b for storage in a Result, guaranteeing the result is valid
// JSON. Captured stream events are echoed back into the run record, and a
// single non-JSON payload (a bare [DONE] sentinel, a provider's stray text)
// would otherwise make the whole record unmarshalable and lose the run.
// Non-JSON payloads are preserved as a JSON string instead.
func safeRaw(b []byte) json.RawMessage {
	if json.Valid(b) {
		return rawCopy(b)
	}
	quoted, err := json.Marshal(string(b))
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(quoted)
}
