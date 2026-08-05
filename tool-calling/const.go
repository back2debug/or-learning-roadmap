package main

import "time"

// ModelName identifies a model on OpenRouter. An empty value means "no model
// specified" — OpenRouter's router picks the model (Parts 4-6).
type ModelName string

const (
	// ModelClaudeSonnet5 is Anthropic's Claude Sonnet 5 as served by OpenRouter.
	ModelClaudeSonnet5 ModelName = "anthropic/claude-sonnet-5"
	// ModelRouted delegates model selection to OpenRouter's Auto Router.
	// Note: omitting the model field entirely returns 400 "No models
	// provided" unless the account has a default model configured, so
	// "openrouter/auto" is the API-level way to say "let OpenRouter decide".
	ModelRouted ModelName = "openrouter/auto"
	// ModelRoutedBeta is OpenRouter's next-generation Auto Router (Beta),
	// which uses a different routing strategy than openrouter/auto.
	ModelRoutedBeta ModelName = "openrouter/auto-beta"
)

// ToolChoice constrains how the model may use tools. Using a dedicated type
// instead of bare strings prevents typos from silently changing behavior.
type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)

// Message roles and finish reasons used by the OpenAI-compatible wire format.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"

	finishReasonToolCalls = "tool_calls"
	finishReasonStop      = "stop"
)

// API endpoint and header constants. Header names are constants so the
// sensitive Authorization header is never assembled from ad-hoc strings.
const (
	openRouterBaseURL   = "https://openrouter.ai/api/v1"
	chatCompletionsPath = "/chat/completions"

	apiKeyEnvVar = "OPENROUTER_API_KEY"
	userAgent    = "openrouter-tool-calling-explorer/1.0 (learning; Go)"

	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	headerUserAgent     = "User-Agent"
	headerTitle         = "X-Title"
	headerRequestID     = "x-request-id"

	contentTypeJSON = "application/json"

	// logFileName is created in the current working directory (the project
	// root when running `go run .` from it), so the full run transcript is
	// easy to find after the program exits.
	logFileName = "tool-calling.log"
)

// Client hardening limits.
const (
	requestTimeout   = 30 * time.Second // per-attempt request budget
	dialTimeout      = 60 * time.Second // TCP dial budget
	maxRetries       = 3                // retries after the first attempt
	baseRetryDelay   = 1 * time.Second  // doubled on each retry
	maxResponseBytes = 10 << 20         // 10 MiB cap on response bodies
	maxConnsPerHost  = 10               // prevent connection exhaustion
	redactKeyPrefix  = 8                // visible chars when logging the key
	maxConversation  = 6                // hard cap on request turns per part
)
