// Package openrouter holds clients and types for OpenRouter's supporting
// routes: GET /models and GET /generation.
package openrouter

// GenerationRecord is the authoritative post-hoc record from
// GET /api/v1/generation?id=. It populates asynchronously; the reconcile
// phase polls with backoff. Pointer fields distinguish "absent" from zero.
type GenerationRecord struct {
	ID                     string   `json:"id"`
	Model                  string   `json:"model"`
	ProviderName           string   `json:"provider_name"`
	TotalCost              *float64 `json:"total_cost"`
	NativeTokensPrompt     *int     `json:"native_tokens_prompt"`
	NativeTokensCompletion *int     `json:"native_tokens_completion"`
	NativeTokensReasoning  *int     `json:"native_tokens_reasoning"`
	NativeTokensCached     *int     `json:"native_tokens_cached"`
	TokensPrompt           *int     `json:"tokens_prompt"`
	TokensCompletion       *int     `json:"tokens_completion"`
	GenerationTimeMS       *int     `json:"generation_time"`
	LatencyMS              *int     `json:"latency"`
	FinishReason           string   `json:"finish_reason"`
	NativeFinishReason     string   `json:"native_finish_reason"`
}
