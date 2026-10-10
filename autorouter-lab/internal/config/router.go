// Package config holds the experiment schema, its strict decoding and
// validation, and the router -> (model slug, plugin id) mapping.
package config

import (
	"errors"
	"fmt"
)

// Router names one of the two Auto Router tracks. Neither is deprecated:
// "auto" is the standard track and "auto-beta" is the early-access track where
// new routing behaviour lands first.
type Router string

const (
	RouterAuto     Router = "auto"
	RouterAutoBeta Router = "auto-beta"
)

// ErrUnknownRouter is returned for any router name outside the two tracks.
var ErrUnknownRouter = errors.New("unknown router")

// Target is what a Router resolves to on the wire.
type Target struct {
	Router    Router
	ModelSlug string
	PluginID  string
}

// targets is the only place in the module where the model slugs and plugin ids
// are spelled out; TestNoStrayRouterLiterals enforces that.
//
// Why this is centralised: each router reads plugin config only under its own
// plugin id. Config sent under the other router's id is accepted and silently
// ignored, with no error or warning, so allowed_models, excluded_models and
// cost_tier simply do nothing and the response looks like a normal routing
// decision. Deriving both strings from one value makes that pairing impossible
// to get wrong by hand.
var targets = []Target{
	{Router: RouterAuto, ModelSlug: "openrouter/auto", PluginID: "auto-router"},
	{Router: RouterAutoBeta, ModelSlug: "openrouter/auto-beta", PluginID: "auto-beta-router"},
}

// Resolve maps a router to its model slug and plugin id.
func Resolve(r Router) (Target, error) {
	for _, t := range targets {
		if t.Router == r {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("%w: %q", ErrUnknownRouter, r)
}

// Routers lists both tracks in a stable order (standard first).
func Routers() []Router {
	out := make([]Router, len(targets))
	for i, t := range targets {
		out[i] = t.Router
	}
	return out
}

// PluginIDForSlug is the reverse lookup used to check bytes already rendered
// for the wire: given a model slug, which plugin id must accompany it.
func PluginIDForSlug(slug string) (string, bool) {
	for _, t := range targets {
		if t.ModelSlug == slug {
			return t.PluginID, true
		}
	}
	return "", false
}

// IsRouterPluginID reports whether id belongs to either Auto Router track.
func IsRouterPluginID(id string) bool {
	for _, t := range targets {
		if t.PluginID == id {
			return true
		}
	}
	return false
}
