//go:build live

package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
	"github.com/OpenRouterTeam/go-sdk/retry"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// TestLiveMismatchedPluginID shows what a silently ignored config looks like.
// It costs real money (two short requests; the third fails before any model
// runs) and only builds with -tags live.
//
// The same restriction, an allow-list no model can match, is sent three ways:
//
//	stable slug + stable id   -> honoured: 404, nothing eligible
//	stable slug + beta id     -> ignored: 200 and an ordinary model
//	beta slug   + stable id   -> ignored: 200 and an ordinary model
//
// The mismatched pairs are built by hand with the plugin-id guard removed,
// because the normal builder and guard make them unreachable. This file is the
// only place that does so.
//
// The plugin id is chosen by picking the SDK constructor, not by setting ID:
// the constructors overwrite ID with their own constant. An earlier version of
// this test set the beta id on the stable plugin type, and the captured bytes
// showed the stable id had gone out instead.
func TestLiveMismatchedPluginID(t *testing.T) {
	if _, err := config.APIKey(); err != nil {
		t.Skip(err)
	}
	auto, _ := config.Resolve(config.RouterAuto)
	beta, _ := config.Resolve(config.RouterAutoBeta)
	impossible := []string{"no-such-vendor-autorouter-lab/*"}

	stablePlugin := components.CreateChatRequestPluginAutoRouter(components.AutoRouterPlugin{AllowedModels: impossible})
	betaPlugin := components.CreateChatRequestPluginAutoBetaRouter(components.AutoBetaRouterPlugin{AllowedModels: impossible})
	cases := []struct {
		name       string
		slug       string
		plugin     components.ChatRequestPlugin
		wantID     string
		wantStatus int
	}{
		{"matched: stable slug, stable id", auto.ModelSlug, stablePlugin, auto.PluginID, 404},
		{"MISMATCHED: stable slug, beta id", auto.ModelSlug, betaPlugin, beta.PluginID, 200},
		{"MISMATCHED: beta slug, stable id", beta.ModelSlug, stablePlugin, auto.PluginID, 200},
	}
	// Only the app-identity guard: the plugin-id guard would block the demo.
	hc := NewHTTPClient(false, []Guard{GuardNoAppIdentity})
	sdk := openrouter.New(openrouter.WithClient(hc), openrouter.WithRetryConfig(retry.Config{Strategy: "none"}))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			ctx, ex := WithExchange(ctx)
			maxTokens := int64(16)
			slug := tc.slug
			_, sdkErr := sdk.Chat.Send(ctx, components.ChatRequest{
				Model:     &slug,
				MaxTokens: optionalnullable.From(&maxTokens),
				Plugins:   []components.ChatRequestPlugin{tc.plugin},
				Messages: []components.ChatMessages{components.CreateChatMessagesUser(components.ChatUserMessage{
					Content: components.CreateChatUserMessageContentStr("Reply with the single word: ok"),
					Role:    components.ChatUserMessageRoleUser,
				})},
			}, components.MetadataLevelEnabled.ToPointer())

			var res TrialResult
			parseResponse(ex.ResponseBody, &res)
			t.Logf("request body:  %s", ex.RequestBody)
			t.Logf("status:        %d", ex.Status)
			t.Logf("model:         %q", res.Model)
			t.Logf("api error:     %s", res.APIError)
			t.Logf("sdk error:     %v", sdkErr)
			var md map[string]json.RawMessage
			if json.Unmarshal(res.Metadata, &md) == nil {
				t.Logf("pipeline:      %s", md["pipeline"])
			}
			// Check the bytes, not the intent: the demo is only valid if the
			// id that went out is the one this case is meant to send.
			if got := sentPluginID(ex.RequestBody); got != tc.wantID {
				t.Fatalf("plugin id on the wire = %q, want %q; this case did not test what it claims", got, tc.wantID)
			}
			if ex.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", ex.Status, tc.wantStatus)
			}
		})
	}
}
