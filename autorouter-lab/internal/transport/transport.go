package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// clientTimeout is the http.Client backstop. Every request also carries a
// context deadline from its TrialSpec; the two are independent on purpose, so
// a missing or over-long context deadline still cannot hang a trial forever.
const clientTimeout = 3 * time.Minute

// Transport sends one trial over one API surface. The runner and the analysis
// only ever see this interface, so neither branches on the surface.
type Transport interface {
	Run(ctx context.Context, spec TrialSpec) (TrialResult, error)
}

// TrialSpec is everything needed to send one trial.
type TrialSpec struct {
	TrialID string
	CellID  string
	Repeat  int
	Router  config.Router
	Prompt  string
	// MaxTokens caps the completion so a trial stays cheap; the selected
	// model, not its answer, is what the experiment measures.
	MaxTokens int
	Plugin    *config.Plugin
	Provider  *config.Provider
	// SessionID is sent in the request body when non-nil. The body is the
	// one path this harness uses; the x-session-id header is never set.
	SessionID *string
	Timeout   time.Duration
}

// TrialResult is the normalized outcome of a trial, the same shape for every
// transport and both routers.
type TrialResult struct {
	TrialID   string
	CellID    string
	Repeat    int
	Router    config.Router
	ModelSlug string
	// PluginID is the plugin id found in the bytes actually sent, empty if
	// no router plugin block was sent.
	PluginID string
	Status   int
	// Model is the model that served the request: the primary dependent
	// variable.
	Model string
	// GenerationID comes from the X-Generation-Id response header; BodyID is
	// the id field of the response body.
	GenerationID string
	BodyID       string
	Usage        json.RawMessage
	// Metadata is the openrouter_metadata block exactly as returned. Its
	// schema is undocumented and may differ between the two tracks, so it is
	// kept raw rather than forced through a struct.
	Metadata json.RawMessage
	// TaskType is data.task_type from the routing pipeline, nil when the
	// response does not carry one (classification unavailable).
	TaskType *string
	// ResolvedTo is data.resolved_to from the pipeline: the dated canonical
	// slug the router chose, where Model is the short public slug.
	ResolvedTo string
	// UpstreamAttempts is how many provider calls OpenRouter made for this
	// one request, from openrouter_metadata.attempts; 0 when not reported,
	// which is the usual case for a first-try success.
	UpstreamAttempts int
	// ModelFallback is true when the attempt that succeeded used a different
	// model from ResolvedTo: the router's choice failed upstream and one of
	// its fallback models answered. Model is then not the routing decision,
	// so analysis of what the router chose must use ResolvedTo.
	ModelFallback bool
	// Provider is the upstream provider named in the response body.
	Provider string
	// CostUSD is usage.cost, nil when the response did not report one.
	CostUSD *float64
	// APIError is the error object of a non-2xx response body.
	APIError json.RawMessage
	// SDKError records an SDK-side failure, such as the generated types
	// rejecting a response shape. It does not invalidate the captured bytes.
	SDKError string
	Latency  time.Duration
	Exchange *Exchange
}

// NewHTTPClient builds the client all transports share: the capture
// RoundTripper over a default transport with certificate verification left on.
func NewHTTPClient(dryRun bool, guards []Guard) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Transport: &Capture{Base: base, Guards: guards, DryRun: dryRun},
		Timeout:   clientTimeout,
	}
}
