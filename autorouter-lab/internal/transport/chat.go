package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
	"github.com/OpenRouterTeam/go-sdk/retry"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// ErrNoResponse means the request was sent, or attempted, and nothing came
// back: a network failure or a timeout. It is the one transport error the
// runner may retry; any other error means the trial could not be sent as
// specified and the run stops.
var ErrNoResponse = errors.New("no response")

// Chat is the Chat Completions transport, built on the OpenRouter Go SDK.
type Chat struct {
	sdk *openrouter.OpenRouter
}

// NewChat builds the transport. baseURL is empty in normal use and points at
// an httptest server in tests.
//
// No security option is passed: the SDK reads OPENROUTER_API_KEY from the
// environment itself, which keeps the key out of this code path. The SDK's
// built-in retry (5xx, backing off for up to an hour) is switched off so the
// runner alone decides what is retried and what it costs.
func NewChat(hc *http.Client, baseURL string) *Chat {
	opts := []openrouter.SDKOption{
		openrouter.WithClient(hc),
		openrouter.WithRetryConfig(retry.Config{Strategy: "none"}),
	}
	if baseURL != "" {
		opts = append(opts, openrouter.WithServerURL(baseURL))
	}
	return &Chat{sdk: openrouter.New(opts...)}
}

// Run sends one trial. A non-2xx response is a result, not an error: a request
// that correctly fails (for example a 404 when restrictions leave no model) is
// data. An error means no response was captured at all: ErrNoResponse for a
// network failure, a guard error if the request was blocked.
func (c *Chat) Run(ctx context.Context, spec TrialSpec) (TrialResult, error) {
	target, err := config.Resolve(spec.Router)
	if err != nil {
		return TrialResult{}, err
	}
	req, err := buildChatRequest(spec, target)
	if err != nil {
		return TrialResult{}, err
	}
	if spec.Timeout <= 0 {
		return TrialResult{}, errors.New("trial has no timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	ctx, ex := WithExchange(ctx)

	// X-OpenRouter-Metadata: enabled asks for the routing pipeline block,
	// which is where data.task_type lives.
	_, sdkErr := c.sdk.Chat.Send(ctx, req, components.MetadataLevelEnabled.ToPointer())

	return finish(spec, target, ex, sdkErr)
}

// finish turns a captured exchange into a normalized result. Both transports
// end here, so a result means the same thing whichever surface produced it.
func finish(spec TrialSpec, target config.Target, ex *Exchange, sendErr error) (TrialResult, error) {
	res := TrialResult{
		TrialID:   spec.TrialID,
		CellID:    spec.CellID,
		Repeat:    spec.Repeat,
		Router:    target.Router,
		ModelSlug: target.ModelSlug,
		PluginID:  sentPluginID(ex.RequestBody),
		Status:    ex.Status,
		Latency:   ex.Latency,
		Exchange:  ex,
	}
	if ex.DryRun {
		return res, ErrDryRun
	}
	if ex.Blocked != nil {
		return res, ex.Blocked
	}
	if ex.Status == 0 {
		return res, fmt.Errorf("%w: %v", ErrNoResponse, sendErr)
	}
	if sendErr != nil {
		res.SDKError = sendErr.Error()
	}
	res.GenerationID = ex.ResponseHeader.Get("X-Generation-Id")
	parseResponse(ex.ResponseBody, &res)
	return res, nil
}

// buildChatRequest renders a spec into the SDK's request type. The model slug
// comes from target; see routerPlugin for how the plugin id is decided.
func buildChatRequest(spec TrialSpec, target config.Target) (components.ChatRequest, error) {
	req := components.ChatRequest{
		Model: &target.ModelSlug,
		Messages: []components.ChatMessages{
			components.CreateChatMessagesUser(components.ChatUserMessage{
				Content: components.CreateChatUserMessageContentStr(spec.Prompt),
				Role:    components.ChatUserMessageRoleUser,
			}),
		},
		SessionID: spec.SessionID,
	}
	if spec.Provider != nil {
		req.Provider = optionalnullable.From(new(buildProvider(*spec.Provider)))
	}
	if spec.MaxTokens > 0 {
		mt := int64(spec.MaxTokens)
		req.MaxTokens = optionalnullable.From(&mt)
	}
	stable, beta, err := routerPlugin(spec.Plugin, target)
	if err != nil {
		return components.ChatRequest{}, err
	}
	switch {
	case stable != nil:
		u := components.CreateChatRequestPluginAutoRouter(*stable)
		if err := checkPluginID(string(u.AutoRouterPlugin.ID), target); err != nil {
			return components.ChatRequest{}, err
		}
		req.Plugins = []components.ChatRequestPlugin{u}
	case beta != nil:
		u := components.CreateChatRequestPluginAutoBetaRouter(*beta)
		if err := checkPluginID(string(u.AutoBetaRouterPlugin.ID), target); err != nil {
			return components.ChatRequest{}, err
		}
		req.Plugins = []components.ChatRequestPlugin{u}
	}
	return req, nil
}

// routerPlugin builds the plugin block for target's router, returning the
// stable type, the beta type, or neither when the spec has no plugin config.
//
// The plugin id is less direct than it looks: the SDK's Create...Plugin
// constructors overwrite the ID field with their own constant, so the id on
// the wire is decided by which constructor the caller uses, not by the value
// assigned here. Callers pick the constructor from which return value is
// non-nil, then pass the rendered id to checkPluginID so the mapping in config
// stays the single authority.
func routerPlugin(p *config.Plugin, target config.Target) (*components.AutoRouterPlugin, *components.AutoBetaRouterPlugin, error) {
	if p == nil {
		return nil, nil, nil
	}
	// The pointer is passed through as a pointer: a nil dial is omitted, a
	// dial of 0 is sent as 0.
	var tradeoff *int64
	if p.CostQualityTradeoff != nil {
		tradeoff = new(int64(*p.CostQualityTradeoff))
	}
	switch target.Router {
	case config.RouterAuto:
		pl := &components.AutoRouterPlugin{
			ID:             components.AutoRouterPluginID(target.PluginID),
			AllowedModels:  p.AllowedModels,
			ExcludedModels: p.ExcludedModels,
			//lint:ignore SA1019 the deprecated dial is itself under study
			CostQualityTradeoff: tradeoff,
		}
		if p.CostTier != nil {
			pl.CostTier = components.AutoRouterPluginCostTier(*p.CostTier).ToPointer()
		}
		return pl, nil, nil
	case config.RouterAutoBeta:
		pl := &components.AutoBetaRouterPlugin{
			ID:             components.AutoBetaRouterPluginID(target.PluginID),
			AllowedModels:  p.AllowedModels,
			ExcludedModels: p.ExcludedModels,
			//lint:ignore SA1019 the deprecated dial is itself under study
			CostQualityTradeoff: tradeoff,
		}
		if p.CostTier != nil {
			pl.CostTier = components.AutoBetaRouterPluginCostTier(*p.CostTier).ToPointer()
		}
		return nil, pl, nil
	default:
		return nil, nil, fmt.Errorf("%w: %q", config.ErrUnknownRouter, target.Router)
	}
}

// checkPluginID fails when the id the SDK rendered is not the one config maps
// the router to: a disagreement (say, after an SDK regeneration) is an error,
// not a request.
func checkPluginID(rendered string, target config.Target) error {
	if rendered != target.PluginID {
		return fmt.Errorf("%w: SDK rendered %q for router %q, want %q", ErrPluginMismatch, rendered, target.Router, target.PluginID)
	}
	return nil
}

// buildProvider maps the provider block field by field. Every optional field
// stays unset unless the config set it, so an explicit false or deny is sent
// and an absent one is not.
func buildProvider(p config.Provider) components.ProviderPreferences {
	var out components.ProviderPreferences
	if len(p.Order) > 0 {
		v := make([]components.ProviderPreferencesOrder, len(p.Order))
		for i, s := range p.Order {
			v[i] = components.CreateProviderPreferencesOrderStr(s)
		}
		out.Order = optionalnullable.From(&v)
	}
	if len(p.Only) > 0 {
		v := make([]components.ProviderPreferencesOnly, len(p.Only))
		for i, s := range p.Only {
			v[i] = components.CreateProviderPreferencesOnlyStr(s)
		}
		out.Only = optionalnullable.From(&v)
	}
	if len(p.Ignore) > 0 {
		v := make([]components.ProviderPreferencesIgnore, len(p.Ignore))
		for i, s := range p.Ignore {
			v[i] = components.CreateProviderPreferencesIgnoreStr(s)
		}
		out.Ignore = optionalnullable.From(&v)
	}
	if p.Sort != nil {
		out.Sort = optionalnullable.From(new(components.CreateProviderPreferencesSortProviderSort(components.ProviderSort(*p.Sort))))
	}
	if m := p.MaxPrice; m != nil {
		// The API schema types these prices as strings.
		out.MaxPrice = &components.MaxPrice{
			Prompt:     priceString(m.Prompt),
			Completion: priceString(m.Completion),
			Request:    priceString(m.Request),
			Image:      priceString(m.Image),
		}
	}
	if p.AllowFallbacks != nil {
		out.AllowFallbacks = optionalnullable.From(p.AllowFallbacks)
	}
	if p.RequireParameters != nil {
		out.RequireParameters = optionalnullable.From(p.RequireParameters)
	}
	if p.DataCollection != nil {
		out.DataCollection = optionalnullable.From(new(components.DataCollection(*p.DataCollection)))
	}
	if p.ZDR != nil {
		out.Zdr = optionalnullable.From(p.ZDR)
	}
	return out
}

func priceString(v *float64) *string {
	if v == nil {
		return nil
	}
	return new(strconv.FormatFloat(*v, 'f', -1, 64))
}

// sentPluginID reads the router plugin id back out of the rendered bytes, so
// the logged value is what was sent rather than what was intended.
func sentPluginID(body []byte) string {
	var doc struct {
		Plugins []struct {
			ID string `json:"id"`
		} `json:"plugins"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	for _, p := range doc.Plugins {
		if config.IsRouterPluginID(p.ID) {
			return p.ID
		}
	}
	return ""
}

// parseResponse pulls the fields the harness needs out of the raw response.
// It reads the captured bytes rather than the SDK's typed result so that a
// field the generated structs do not know about is not lost, and it tolerates
// any field being missing or the wrong type.
func parseResponse(body []byte, res *TrialResult) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return
	}
	res.BodyID = jsonString(doc["id"])
	res.Model = jsonString(doc["model"])
	res.Usage = doc["usage"]
	res.Metadata = doc["openrouter_metadata"]
	res.APIError = doc["error"]
	res.Provider = jsonString(doc["provider"])
	res.TaskType, res.ResolvedTo = pipelineFields(res.Metadata)
	res.UpstreamAttempts, res.ModelFallback = upstreamAttempts(res.Metadata, res.ResolvedTo)

	var usage struct {
		Cost *float64 `json:"cost"`
	}
	if json.Unmarshal(res.Usage, &usage) == nil && usage.Cost != nil && *usage.Cost >= 0 {
		res.CostUSD = usage.Cost
	}
}

// pipelineFields finds data.task_type and data.resolved_to in any stage of
// openrouter_metadata.pipeline. The task type is nil when no stage carries
// one, which the docs describe as the sign that classification was
// unavailable.
func pipelineFields(metadata json.RawMessage) (taskType *string, resolvedTo string) {
	var md struct {
		Pipeline []struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"pipeline"`
	}
	if json.Unmarshal(metadata, &md) != nil {
		return nil, ""
	}
	for _, stage := range md.Pipeline {
		if tt := jsonString(stage.Data["task_type"]); tt != "" && taskType == nil {
			taskType = &tt
		}
		if r := jsonString(stage.Data["resolved_to"]); r != "" && resolvedTo == "" {
			resolvedTo = r
		}
	}
	return taskType, resolvedTo
}

// upstreamAttempts reads openrouter_metadata.attempts, the list of provider
// calls made for one request, and reports whether the last one (the one that
// answered) ran a different model from the router's choice.
func upstreamAttempts(metadata json.RawMessage, resolvedTo string) (n int, fallback bool) {
	var md struct {
		Attempts []struct {
			Model string `json:"model"`
		} `json:"attempts"`
	}
	if json.Unmarshal(metadata, &md) != nil || len(md.Attempts) == 0 {
		return 0, false
	}
	last := md.Attempts[len(md.Attempts)-1].Model
	return len(md.Attempts), resolvedTo != "" && last != "" && last != resolvedTo
}

// jsonString decodes a JSON string, returning "" for anything else.
func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
