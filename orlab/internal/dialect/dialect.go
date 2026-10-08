// Package dialect defines the canonical prompt/result model and the Dialect
// interface implemented by the three OpenRouter API formats.
//
// Two boundaries make cross-dialect comparison meaningful:
//
//	PromptSpec  --BuildRequest-->  dialect-specific JSON body
//	dialect-specific response --ParseStream/ParseResponse-->  Result
//
// No dialect-specific type leaks past this package's exported surface.
// Streaming is the primary path: debug.echo_upstream_body only exists on
// streamed responses, so ParseStream is the code path every default run
// exercises; ParseResponse serves the buffered (--no-stream) comparison mode.
package dialect

import (
	"encoding/json"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/openrouter"
)

// Kind labels what a suite entry isolates. Values mirror the prompt suite
// table in the README (plain, system, multiturn, prefill, json_schema, tools,
// reasoning, stop_seq, param_fate, unicode, long_ctx, buffered,
// extras_parity, error_probe).
type Kind string

// Role of a conversation turn.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Turn is one conversation message. A trailing assistant turn is a prefill.
type Turn struct {
	Role Role
	// Text content only. The suite keeps prompts benign and cheap; multimodal
	// content is out of scope.
	Text string
	// ToolCallID marks this turn as a tool result for that call (two-round
	// tool trips). Empty otherwise.
	ToolCallID string
	// ToolCalls present on an assistant turn replaying a prior tool call.
	ToolCalls []ToolCall
}

// ToolDef is a dialect-independent tool description. Each dialect renders it
// into its own schema shape (tools[].function vs custom tools vs Responses
// tools).
type ToolDef struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object, stored raw so it round-trips
	// byte-stably into every dialect.
	Parameters json.RawMessage
}

// ToolChoice normalizes tool-forcing across dialects.
type ToolChoice struct {
	Mode string // "", "auto", "none", "required", "named"
	Name string // set when Mode == "named"
}

// Schema is a JSON-schema structured-output request.
type Schema struct {
	Name   string
	Strict bool
	Schema json.RawMessage
}

// PromptSpec is the dialect-independent description of one request.
type PromptSpec struct {
	ID string
	// Model is the OpenRouter slug. The runner clones the spec once per
	// model under test.
	Model  string
	Kind   Kind
	System string
	Turns  []Turn

	Tools      []ToolDef
	ToolChoice ToolChoice
	JSONSchema *Schema

	MaxTokens     int
	Temperature   *float64
	TopP          *float64
	TopK          *int // the star witness for silent-drop detection
	StopSequences []string
	Seed          *int
	// ReasoningEffort in chat's vocabulary (max|xhigh|high|medium|low|minimal|none).
	// Messages translates it to thinking budgets; Responses to its reasoning field.
	ReasoningEffort string
	Stream          bool

	// Extras exercised by the extras_parity / error_probe entries. They are
	// injected as-is into the built body so probes can send fields a dialect
	// "shouldn't" accept (e.g. output_config on chat, a 200-char session_id).
	SessionID    string
	OutputConfig json.RawMessage
	Metadata     map[string]string

	// EchoUpstream requests debug.echo_upstream_body (streaming only).
	EchoUpstream bool
	// RequireParameters sets provider.require_parameters, flipping silent
	// parameter drops into routing failures.
	RequireParameters *bool
}

// Usage is the normalized token/cost accounting from the inline usage block.
// Pointers distinguish "not reported by this dialect" from zero — silent
// zeros destroy comparisons.
type Usage struct {
	PromptTokens     *int
	CompletionTokens *int
	ReasoningTokens  *int
	CachedTokens     *int
	Cost             *float64
}

// ToolCall is a normalized tool invocation from the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// StreamEvent is one raw SSE event, kept for the streaming-grammar report.
type StreamEvent struct {
	// Type is the event's own type label: the SSE "event:" field on dialects
	// that use it (messages, responses) or the JSON "type"/chunk shape on
	// chat. Empty when the dialect emits untyped data-only chunks.
	Type string
	// Data is the raw (redacted-at-write) payload.
	Data json.RawMessage
	At   time.Duration // offset from request start
}

// Result is everything a dialect must be able to report back.
type Result struct {
	Text          string
	ReasoningText string
	ToolCalls     []ToolCall

	StopReason       string // normalized: stop|length|tool_use|content_filter|error|other
	NativeStopReason string // raw, as the dialect reported it

	Usage        Usage
	Provider     string
	GenerationID string

	TTFB  time.Duration
	Total time.Duration

	// UpstreamBody from debug.echo_upstream_body; nil when absent. Multiple
	// provider attempts produce multiple bodies.
	UpstreamBodies []json.RawMessage
	// RouterMetadata from openrouter_metadata; nil when absent.
	RouterMetadata json.RawMessage
	// Reconciled from GET /generation, filled by the reconcile phase.
	Reconciled *openrouter.GenerationRecord

	// Unsupported records fields this dialect cannot populate, keyed by
	// Result field name with a human-readable reason. An entry here is the
	// explicit unsupported marker; consumers must check it before reading a
	// zero-valued field.
	Unsupported map[string]string
}

// Request is a fully built, dialect-specific HTTP request in pure-data form.
// Keeping it data (not *http.Request) makes the Build phase diffable and the
// dialects trivially testable; the runner owns turning it into an
// *http.Request against the configured base URL.
type Request struct {
	// Path relative to the API base, e.g. "/chat/completions".
	Path string
	// Body is the exact JSON to send.
	Body []byte
	// Header carries dialect- or feature-specific headers beyond the
	// standard set (Authorization/Content-Type are the runner's job).
	// X-OpenRouter-Metadata lives here.
	Header map[string]string
}

// Dialect translates specs to requests and responses to results. One
// implementation per OpenRouter API format. Implementations must be
// stateless and safe for concurrent use.
type Dialect interface {
	// Name is the stable identifier: "chat", "responses", "messages".
	Name() string

	// BuildRequest renders spec into this dialect's wire format. It must be
	// deterministic: identical specs yield byte-identical bodies, so Build
	// diffs are stable. Fields the dialect cannot express are reported in
	// the returned BuildNotes, never silently dropped.
	BuildRequest(spec PromptSpec) (Request, BuildNotes, error)

	// ParseStream consumes an SSE stream (primary path — echo data only
	// exists here). It returns the normalized result, the raw event log for
	// the streaming-grammar report, and an error only for malformed streams;
	// an in-stream error event becomes part of the Result via StopReason
	// "error" plus the APIError in events.
	ParseStream(r StreamReader) (*Result, []StreamEvent, error)

	// ParseResponse parses a buffered (non-streamed) response body.
	ParseResponse(body []byte) (*Result, error)
}

// BuildNotes records what translation did to the spec: canonical fields the
// dialect had to rename, transform, or could not express. This feeds the
// Build-phase diff and the parameter-fate table.
type BuildNotes struct {
	// Renamed maps canonical field -> wire field (e.g. MaxTokens ->
	// "max_completion_tokens" on chat, "max_output_tokens" on responses).
	Renamed map[string]string
	// Transformed maps canonical field -> description of a non-trivial
	// translation (e.g. ReasoningEffort -> thinking.budget_tokens=8192).
	Transformed map[string]string
	// Unexpressible lists canonical fields this dialect has no wire form
	// for, with reasons.
	Unexpressible map[string]string
}

// StreamReader is what ParseStream consumes: a line-oriented reader plus the
// clock needed to stamp TTFB. The runner supplies both; tests supply fakes.
type StreamReader interface {
	// ReadLine returns the next line of the SSE stream without the trailing
	// newline. io.EOF ends the stream.
	ReadLine() ([]byte, error)
	// Elapsed reports time since the request was sent (for TTFB and event
	// offsets).
	Elapsed() time.Duration
}
