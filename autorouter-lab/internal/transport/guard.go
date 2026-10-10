package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

var (
	// ErrAppIdentity means a request carried a header that would label this
	// project in OpenRouter's public app rankings.
	ErrAppIdentity = errors.New("app identity header present")
	// ErrPluginMismatch means a router plugin id does not belong to the
	// model slug it was sent with.
	ErrPluginMismatch = errors.New("plugin id does not match model slug")
)

// appIdentityHeaders label the calling app. HTTP-Referer is included because
// OpenRouter uses it as the primary identifier for rankings; a title alone is
// only the display name.
var appIdentityHeaders = []string{"X-Title", "X-OpenRouter-Title", "HTTP-Referer", "X-OpenRouter-Categories"}

// GuardNoAppIdentity blocks any request carrying an app identity header.
//
// The harness never sets one. The OpenRouter SDK does read OPENROUTER_X_TITLE
// and OPENROUTER_HTTP_REFERER from the environment into its global options;
// v0.9.40 does not apply those globals to chat completions, but that is a
// detail of generated code that can change on any release, so the rendered
// request is checked rather than the SDK trusted.
func GuardNoAppIdentity(req *http.Request, _ []byte) error {
	for _, h := range appIdentityHeaders {
		if req.Header.Get(h) != "" {
			return fmt.Errorf("%w: %s (unset OPENROUTER_X_TITLE and OPENROUTER_HTTP_REFERER)", ErrAppIdentity, h)
		}
	}
	return nil
}

// GuardPluginID blocks a request whose body pairs a router model slug with the
// other router's plugin id.
//
// That pairing is accepted by the API and silently ignored, which would make
// every restricted cell look like a legitimate unrestricted routing decision.
// The payload builder cannot produce it, and this check on the rendered bytes
// means nothing else can either.
func GuardPluginID(req *http.Request, body []byte) error {
	if req.Method != http.MethodPost || len(body) == 0 {
		return nil
	}
	var doc struct {
		Model   string `json:"model"`
		Plugins []struct {
			ID string `json:"id"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("request body is not a JSON object: %w", err)
	}
	want, isRouter := config.PluginIDForSlug(doc.Model)
	for _, p := range doc.Plugins {
		if !config.IsRouterPluginID(p.ID) {
			continue
		}
		if !isRouter || p.ID != want {
			return fmt.Errorf("%w: model %q with plugin %q", ErrPluginMismatch, doc.Model, p.ID)
		}
	}
	return nil
}

// DefaultGuards are the guards every normal request passes through.
func DefaultGuards() []Guard {
	return []Guard{GuardNoAppIdentity, GuardPluginID}
}
