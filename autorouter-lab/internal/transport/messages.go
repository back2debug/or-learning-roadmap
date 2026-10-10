package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// DefaultBaseURL is OpenRouter's API root.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// Messages is the transport for OpenRouter's Anthropic-compatible Messages
// endpoint.
//
// The OpenRouter Go SDK (v0.9.40) defines the Messages request type, with
// typed plugins, provider and session_id, but has no method that sends one:
// its only use of the type is saving a preset. So the body is built with the
// SDK's type, which keeps the same plugin constructors as Chat Completions,
// and the POST is made here with net/http through the shared capture client.
//
// That makes this the one place where harness code handles the API key: it
// sets the Authorization header itself. The key still comes only from the
// environment, and the redactor scrubs it from every log.
type Messages struct {
	hc     *http.Client
	url    string
	apiKey string
}

// NewMessages builds the transport. baseURL is DefaultBaseURL in normal use
// and an httptest server in tests.
func NewMessages(hc *http.Client, baseURL, apiKey string) (*Messages, error) {
	if apiKey == "" {
		return nil, config.ErrNoAPIKey
	}
	return &Messages{hc: hc, url: strings.TrimRight(baseURL, "/") + "/messages", apiKey: apiKey}, nil
}

// Run sends one trial. As with Chat, a non-2xx response is a result.
func (m *Messages) Run(ctx context.Context, spec TrialSpec) (TrialResult, error) {
	target, err := config.Resolve(spec.Router)
	if err != nil {
		return TrialResult{}, err
	}
	payload, err := buildMessagesRequest(spec, target)
	if err != nil {
		return TrialResult{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return TrialResult{}, fmt.Errorf("messages: encode request: %w", err)
	}
	if spec.Timeout <= 0 {
		return TrialResult{}, errors.New("trial has no timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	ctx, ex := WithExchange(ctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return TrialResult{}, fmt.Errorf("messages: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Same opt-in as Chat Completions; whether this surface honours it is
	// part of what the experiment observes.
	req.Header.Set("X-OpenRouter-Metadata", "enabled")

	resp, sendErr := m.hc.Do(req)
	if sendErr == nil {
		// Capture has already buffered the body into the exchange.
		_, _ = io.Copy(io.Discard, resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			sendErr = cerr
		}
	}
	return finish(spec, target, ex, sendErr)
}

// buildMessagesRequest renders a spec into the SDK's Messages request type.
// max_tokens is required by this API, unlike Chat Completions.
func buildMessagesRequest(spec TrialSpec, target config.Target) (components.MessagesRequest, error) {
	if spec.MaxTokens <= 0 {
		return components.MessagesRequest{}, errors.New("messages: max_tokens is required")
	}
	req := components.MessagesRequest{
		Model:     target.ModelSlug,
		MaxTokens: new(int64(spec.MaxTokens)),
		Messages: []components.MessagesMessageParam{{
			Role:    components.MessagesMessageParamRoleUser,
			Content: components.CreateMessagesMessageParamContentUnion5Str(spec.Prompt),
		}},
		SessionID: spec.SessionID,
	}
	if spec.Provider != nil {
		req.Provider = optionalnullable.From(new(buildProvider(*spec.Provider)))
	}
	stable, beta, err := routerPlugin(spec.Plugin, target)
	if err != nil {
		return components.MessagesRequest{}, err
	}
	switch {
	case stable != nil:
		u := components.CreateMessagesRequestPluginAutoRouter(*stable)
		if err := checkPluginID(string(u.AutoRouterPlugin.ID), target); err != nil {
			return components.MessagesRequest{}, err
		}
		req.Plugins = []components.MessagesRequestPlugin{u}
	case beta != nil:
		u := components.CreateMessagesRequestPluginAutoBetaRouter(*beta)
		if err := checkPluginID(string(u.AutoBetaRouterPlugin.ID), target); err != nil {
			return components.MessagesRequest{}, err
		}
		req.Plugins = []components.MessagesRequestPlugin{u}
	}
	return req, nil
}
