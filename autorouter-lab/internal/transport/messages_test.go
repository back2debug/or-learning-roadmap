package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

const messagesOKBody = `{"id":"msg_01","type":"message","role":"assistant","model":"vendor/some-model",` +
	`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cost":0.0002},` +
	`"openrouter_metadata":{"pipeline":[{"type":"plugin","data":{"task_type":"math","resolved_to":"vendor/some-model-20260101"}}]}}`

func newMessages(t *testing.T, s *server, dryRun bool) *Messages {
	t.Helper()
	m, err := NewMessages(NewHTTPClient(dryRun, DefaultGuards()), s.URL, fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessagesNeedsKey(t *testing.T) {
	if _, err := NewMessages(http.DefaultClient, DefaultBaseURL, ""); !errors.Is(err, config.ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestMessagesRequestShape(t *testing.T) {
	s := newServer(t, 200, messagesOKBody)
	tier := config.CostTier("low")
	res, err := newMessages(t, s, false).Run(context.Background(), spec(config.RouterAutoBeta, &config.Plugin{CostTier: &tier}))
	if err != nil {
		t.Fatal(err)
	}
	got := s.lastReq.Load()
	if got.Method != http.MethodPost || got.URL.Path != "/messages" {
		t.Errorf("request = %s %s, want POST /messages", got.Method, got.URL.Path)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer "+fakeKey {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.Header.Get("X-OpenRouter-Metadata"); h != "enabled" {
		t.Errorf("X-OpenRouter-Metadata = %q", h)
	}
	if got.Header.Get("X-Session-Id") != "" {
		t.Error("x-session-id header set; the body is the only session path")
	}
	for _, h := range appIdentityHeaders {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("app identity header %s = %q", h, v)
		}
	}
	if string(*s.body.Load()) != string(res.Exchange.RequestBody) {
		t.Fatal("captured body differs from received body")
	}
	want := `{"max_tokens":16,"messages":[{"content":"what is 2+2","role":"user"}],"model":"openrouter/auto-beta",` +
		`"plugins":[{"cost_tier":"low","id":"auto-beta-router"}],"session_id":"arl-test-session"}`
	if string(res.Exchange.RequestBody) != want {
		t.Errorf("body = %s\n want %s", res.Exchange.RequestBody, want)
	}
	if res.Model != "vendor/some-model" || res.BodyID != "msg_01" || res.GenerationID != "gen-header-1" ||
		res.ResolvedTo != "vendor/some-model-20260101" || res.PluginID != "auto-beta-router" ||
		res.TaskType == nil || *res.TaskType != "math" || res.CostUSD == nil || *res.CostUSD != 0.0002 {
		t.Errorf("result = %+v", res)
	}
	if res.Exchange.Attempts != 1 || s.hits.Load() != 1 {
		t.Errorf("attempts = %d, hits = %d", res.Exchange.Attempts, s.hits.Load())
	}
}

// The routing config the two transports send must be identical for the same
// spec, or a difference between surfaces could be the harness's own doing.
func TestTransportsSendSameRoutingConfig(t *testing.T) {
	tier := config.CostTier("xhigh")
	specs := map[string]TrialSpec{}
	add := func(name string, r config.Router, p *config.Plugin, prov *config.Provider) {
		sp := spec(r, p)
		sp.Provider = prov
		specs[name] = sp
	}
	for _, r := range config.Routers() {
		add(string(r)+"/none", r, nil, nil)
		add(string(r)+"/tier", r, &config.Plugin{CostTier: &tier}, nil)
		add(string(r)+"/dial zero", r, &config.Plugin{CostQualityTradeoff: ptr(0)}, nil)
		add(string(r)+"/both", r, &config.Plugin{CostTier: &tier, CostQualityTradeoff: ptr(10)}, nil)
		add(string(r)+"/lists", r, &config.Plugin{AllowedModels: []string{"google/*"}, ExcludedModels: []string{"*flash*"}}, nil)
		add(string(r)+"/provider", r, nil, &config.Provider{MaxPrice: &config.MaxPrice{Prompt: ptr(25.0), Completion: ptr(125.0)}, ZDR: ptr(false), DataCollection: ptr("deny")})
	}
	type routing struct {
		Model     string          `json:"model"`
		Plugins   json.RawMessage `json:"plugins"`
		Provider  json.RawMessage `json:"provider"`
		SessionID *string         `json:"session_id"`
	}
	extract := func(t *testing.T, body []byte) (routing, string) {
		t.Helper()
		var r routing
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
		flat, _ := json.Marshal(r)
		return r, string(flat)
	}
	for name, sp := range specs {
		t.Run(name, func(t *testing.T) {
			cs := newServer(t, 200, okBody)
			chatRes, err := newChat(t, cs, true).Run(context.Background(), sp)
			if !errors.Is(err, ErrDryRun) {
				t.Fatal(err)
			}
			ms := newServer(t, 200, messagesOKBody)
			msgRes, err := newMessages(t, ms, true).Run(context.Background(), sp)
			if !errors.Is(err, ErrDryRun) {
				t.Fatal(err)
			}
			cr, cflat := extract(t, chatRes.Exchange.RequestBody)
			_, mflat := extract(t, msgRes.Exchange.RequestBody)
			if cflat != mflat {
				t.Errorf("routing config differs:\n chat     %s\n messages %s", cflat, mflat)
			}
			if sp.Plugin != nil {
				want, _ := config.PluginIDForSlug(cr.Model)
				if msgRes.PluginID != want || chatRes.PluginID != want {
					t.Errorf("plugin ids chat %q messages %q, want %q", chatRes.PluginID, msgRes.PluginID, want)
				}
				if sp.Plugin.CostQualityTradeoff != nil && *sp.Plugin.CostQualityTradeoff == 0 &&
					!strings.Contains(string(msgRes.Exchange.RequestBody), `"cost_quality_tradeoff":0`) {
					t.Errorf("zero dial missing from messages body: %s", msgRes.Exchange.RequestBody)
				}
			}
			if cs.hits.Load()+ms.hits.Load() != 0 {
				t.Error("dry run reached a server")
			}
		})
	}
}

func TestMessagesErrorStatusIsAResult(t *testing.T) {
	body := `{"type":"error","error":{"type":"not_found_error","message":"No models match your request and model restrictions"}}`
	s := newServer(t, 404, body)
	res, err := newMessages(t, s, false).Run(context.Background(), spec(config.RouterAuto, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 404 || !strings.Contains(string(res.APIError), "No models match") || s.hits.Load() != 1 {
		t.Errorf("status %d error %s hits %d", res.Status, res.APIError, s.hits.Load())
	}
}

func TestMessagesFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	m, err := NewMessages(NewHTTPClient(false, DefaultGuards()), slow.URL, fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	sp := spec(config.RouterAuto, nil)
	sp.Timeout = 100 * time.Millisecond
	if _, err := m.Run(context.Background(), sp); !errors.Is(err, ErrNoResponse) {
		t.Errorf("timeout: err = %v, want ErrNoResponse", err)
	}
	sp.Timeout = 0
	if _, err := m.Run(context.Background(), sp); err == nil {
		t.Error("a trial without a timeout was accepted")
	}
	sp.Timeout = time.Second
	sp.MaxTokens = 0
	if _, err := m.Run(context.Background(), sp); err == nil {
		t.Error("a Messages trial without max_tokens was accepted")
	}
	sp.MaxTokens = 16
	sp.Router = "nope"
	if _, err := m.Run(context.Background(), sp); !errors.Is(err, config.ErrUnknownRouter) {
		t.Errorf("unknown router: err = %v", err)
	}
}
