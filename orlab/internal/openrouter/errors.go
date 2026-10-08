package openrouter

import (
	"encoding/json"
	"fmt"
)

// APIError is OpenRouter's error envelope, parsed leniently so shape
// differences across dialects are recorded rather than lost.
type APIError struct {
	Status  int    `json:"status"`
	Code    string `json:"code,omitempty"`    // error.code, stringified (number or string on the wire)
	Message string `json:"message,omitempty"` // error.message
	// ErrorType is error.metadata.error_type — the "canonical OpenRouter
	// error type, stable across all API formats" claim under test.
	ErrorType string `json:"error_type,omitempty"`
	// ErrorTypePath records WHERE error_type was found, since that location
	// is not consistent across dialects. Empty when it was absent entirely.
	ErrorTypePath string `json:"error_type_path,omitempty"`
	// NativeType is the dialect's own classifier (Anthropic's error.type).
	NativeType   string `json:"native_type,omitempty"`
	ProviderCode string `json:"provider_code,omitempty"`
	// Metadata is error.metadata verbatim.
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// RouterMetadata is the top-level openrouter_metadata sibling of error.
	RouterMetadata json.RawMessage `json:"openrouter_metadata,omitempty"`
	// Raw is the whole body, for taxonomy comparison of envelope shapes.
	Raw json.RawMessage `json:"raw,omitempty"`
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "(no message)"
	}
	if e.ErrorType != "" {
		return fmt.Sprintf("openrouter: %d %s: %s", e.Status, e.ErrorType, msg)
	}
	return fmt.Sprintf("openrouter: %d: %s", e.Status, msg)
}

// ParseAPIError builds an APIError from a non-2xx body. It never fails: an
// unparseable body still yields a usable error with Raw set.
func ParseAPIError(status int, body []byte) *APIError {
	out := &APIError{Status: status, Raw: json.RawMessage(append([]byte(nil), body...))}
	var env struct {
		Error struct {
			Code     json.RawMessage `json:"code"`
			Message  string          `json:"message"`
			Metadata json.RawMessage `json:"metadata"`
			// Observed 2026-08-28: /messages returns an Anthropic-shaped
			// envelope carrying error_type as a direct sibling of message,
			// not under metadata as the docs describe. Read both.
			ErrorType string `json:"error_type"`
			// Type is Anthropic's own classifier (e.g. invalid_request_error).
			Type string `json:"type"`
		} `json:"error"`
		RouterMetadata json.RawMessage `json:"openrouter_metadata"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		out.Message = string(body)
		return out
	}
	out.Message = env.Error.Message
	out.Metadata = env.Error.Metadata
	out.RouterMetadata = env.RouterMetadata
	if len(env.Error.Code) > 0 {
		var s string
		if json.Unmarshal(env.Error.Code, &s) == nil {
			out.Code = s
		} else {
			out.Code = string(env.Error.Code)
		}
	}
	out.NativeType = env.Error.Type
	if len(env.Error.Metadata) > 0 {
		var meta struct {
			ErrorType    string `json:"error_type"`
			ProviderCode string `json:"provider_code"`
		}
		if json.Unmarshal(env.Error.Metadata, &meta) == nil {
			out.ErrorType = meta.ErrorType
			out.ProviderCode = meta.ProviderCode
			if meta.ErrorType != "" {
				out.ErrorTypePath = "error.metadata.error_type"
			}
		}
	}
	if out.ErrorType == "" && env.Error.ErrorType != "" {
		out.ErrorType = env.Error.ErrorType
		out.ErrorTypePath = "error.error_type"
	}
	return out
}
