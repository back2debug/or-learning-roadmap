package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

const fakeKey = "sk-or-v1-testtesttesttesttesttest"

const okBody = `{"id":"gen-body-1","model":"vendor/some-model","usage":{"prompt_tokens":5,"cost":0.0001},` +
	`"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","index":0}],"created":1,"object":"chat.completion",` +
	`"openrouter_metadata":{"pipeline":[{"type":"guardrail"},{"type":"router","data":{"task_type":"math","future_field":7}}],"unknown_block":{"a":1}}}`

type server struct {
	*httptest.Server
	hits    atomic.Int32
	lastReq atomic.Pointer[http.Request]
	body    atomic.Pointer[[]byte]
}

func newServer(t *testing.T, status int, respBody string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s.body.Store(&b)
		s.lastReq.Store(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Generation-Id", "gen-header-1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(s.Close)
	return s
}

func newChat(t *testing.T, s *server, dryRun bool) *Chat {
	t.Helper()
	t.Setenv(config.APIKeyEnv, fakeKey)
	t.Setenv("OPENROUTER_X_TITLE", "")
	t.Setenv("OPENROUTER_HTTP_REFERER", "")
	return NewChat(NewHTTPClient(dryRun, DefaultGuards()), s.URL)
}

func spec(r config.Router, p *config.Plugin) TrialSpec {
	sid := "arl-test-session"
	return TrialSpec{TrialID: "t1", CellID: "c1", Router: r, Prompt: "what is 2+2", MaxTokens: 16, Plugin: p, SessionID: &sid, Timeout: 5 * time.Second}
}

func ptr[T any](v T) *T { return &v }

// Every payload the builder renders must pair its model slug with that
// router's own plugin id, for both routers and every plugin shape.
func TestWirePayload(t *testing.T) {
	tier := config.CostTier("high")
	plugins := map[string]*config.Plugin{
		"none":      nil,
		"tier":      {CostTier: &tier},
		"dial zero": {CostQualityTradeoff: ptr(0)},
		"dial ten":  {CostQualityTradeoff: ptr(10)},
		"lists":     {AllowedModels: []string{"anthropic/*"}, ExcludedModels: []string{"*/claude-*"}},
		"both cost": {CostTier: &tier, CostQualityTradeoff: ptr(3)},
	}
	for _, router := range config.Routers() {
		for name, plugin := range plugins {
			t.Run(string(router)+"/"+name, func(t *testing.T) {
				s := newServer(t, 200, okBody)
				res, err := newChat(t, s, false).Run(context.Background(), spec(router, plugin))
				if err != nil {
					t.Fatal(err)
				}
				target, _ := config.Resolve(router)

				// The capture must hold the same bytes the server received.
				if got := string(*s.body.Load()); got != string(res.Exchange.RequestBody) {
					t.Fatalf("captured body differs from received body:\n captured %s\n received %s", res.Exchange.RequestBody, got)
				}
				var wire struct {
					Model     string  `json:"model"`
					SessionID *string `json:"session_id"`
					Plugins   []struct {
						ID                  string   `json:"id"`
						CostTier            *string  `json:"cost_tier"`
						CostQualityTradeoff *int     `json:"cost_quality_tradeoff"`
						AllowedModels       []string `json:"allowed_models"`
						ExcludedModels      []string `json:"excluded_models"`
					} `json:"plugins"`
				}
				if err := json.Unmarshal(res.Exchange.RequestBody, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Model != target.ModelSlug {
					t.Errorf("model = %q, want %q", wire.Model, target.ModelSlug)
				}
				if wire.SessionID == nil || *wire.SessionID != "arl-test-session" {
					t.Errorf("session_id not in body: %s", res.Exchange.RequestBody)
				}
				if plugin == nil {
					if len(wire.Plugins) != 0 || res.PluginID != "" {
						t.Fatalf("plugins sent with no plugin config: %s", res.Exchange.RequestBody)
					}
					return
				}
				if len(wire.Plugins) != 1 {
					t.Fatalf("got %d plugins, want 1: %s", len(wire.Plugins), res.Exchange.RequestBody)
				}
				got := wire.Plugins[0]
				want, _ := config.PluginIDForSlug(wire.Model)
				if got.ID != want || res.PluginID != want {
					t.Errorf("plugin id = %q (result %q), want %q for model %q", got.ID, res.PluginID, want, wire.Model)
				}
				if (plugin.CostTier == nil) != (got.CostTier == nil) || (got.CostTier != nil && *got.CostTier != string(*plugin.CostTier)) {
					t.Errorf("cost_tier on wire = %v, config = %v", got.CostTier, plugin.CostTier)
				}
				// Pointer semantics: nil is absent, 0 is sent as 0.
				if (plugin.CostQualityTradeoff == nil) != (got.CostQualityTradeoff == nil) {
					t.Errorf("cost_quality_tradeoff presence: wire %v, config %v in %s", got.CostQualityTradeoff, plugin.CostQualityTradeoff, res.Exchange.RequestBody)
				} else if got.CostQualityTradeoff != nil && *got.CostQualityTradeoff != *plugin.CostQualityTradeoff {
					t.Errorf("cost_quality_tradeoff = %d, want %d", *got.CostQualityTradeoff, *plugin.CostQualityTradeoff)
				}
				if strings.Join(got.AllowedModels, ",") != strings.Join(plugin.AllowedModels, ",") ||
					strings.Join(got.ExcludedModels, ",") != strings.Join(plugin.ExcludedModels, ",") {
					t.Errorf("model lists differ: %s", res.Exchange.RequestBody)
				}
			})
		}
	}
}

func TestRunCapturesExchangeAndParsesResponse(t *testing.T) {
	s := newServer(t, 200, okBody)
	res, err := newChat(t, s, false).Run(context.Background(), spec(config.RouterAutoBeta, nil))
	if err != nil {
		t.Fatal(err)
	}
	ex := res.Exchange
	if ex.Attempts != 1 || ex.Status != 200 || ex.Method != "POST" || !strings.HasSuffix(ex.URL, "/chat/completions") {
		t.Errorf("exchange = attempts %d status %d %s %s", ex.Attempts, ex.Status, ex.Method, ex.URL)
	}
	if string(ex.ResponseBody) != okBody {
		t.Errorf("response body not captured exactly")
	}
	if ex.Latency <= 0 {
		t.Error("latency not recorded")
	}
	if got := ex.RequestHeader.Get("Authorization"); got != "Bearer "+fakeKey {
		t.Errorf("SDK did not pick the key up from the environment: Authorization = %q", got)
	}
	if got := ex.RequestHeader.Get("X-OpenRouter-Metadata"); got != "enabled" {
		t.Errorf("X-OpenRouter-Metadata = %q, want enabled", got)
	}
	if ex.RequestHeader.Get("X-Session-Id") != "" {
		t.Error("x-session-id header set; the body is the only session path")
	}
	for _, h := range appIdentityHeaders {
		if v := s.lastReq.Load().Header.Get(h); v != "" {
			t.Errorf("server received app identity header %s = %q", h, v)
		}
	}
	if res.Model != "vendor/some-model" || res.BodyID != "gen-body-1" || res.GenerationID != "gen-header-1" {
		t.Errorf("result = model %q body id %q generation id %q", res.Model, res.BodyID, res.GenerationID)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.0001 {
		t.Errorf("cost = %v, want 0.0001", res.CostUSD)
	}
	if res.TaskType == nil || *res.TaskType != "math" {
		t.Errorf("task type = %v, want math", res.TaskType)
	}
	// Fields the harness has never heard of must survive in the raw block.
	if !strings.Contains(string(res.Metadata), `"future_field":7`) || !strings.Contains(string(res.Metadata), `"unknown_block"`) {
		t.Errorf("raw metadata lost fields: %s", res.Metadata)
	}
}

// Responses are untrusted: odd shapes must not panic or invent values.
func TestParseResponseTolerant(t *testing.T) {
	bodies := []string{
		``, `null`, `[]`, `"text"`, `{`, `{}`,
		`{"model":7,"id":null,"usage":"x","openrouter_metadata":[]}`,
		`{"openrouter_metadata":{"pipeline":"nope"}}`,
		`{"usage":{"cost":"free"}}`, `{"usage":{"cost":-1}}`, `{"usage":{"cost":null}}`,
		`{"openrouter_metadata":{"pipeline":[null,{"data":null},{"data":{"task_type":5}},{"data":{"task_type":""}}]}}`,
	}
	for _, b := range bodies {
		var res TrialResult
		parseResponse([]byte(b), &res)
		if res.Model != "" || res.BodyID != "" || res.TaskType != nil || res.CostUSD != nil || res.ResolvedTo != "" {
			t.Errorf("body %q produced model %q id %q task %v", b, res.Model, res.BodyID, res.TaskType)
		}
	}
}

func TestUpstreamAttempts(t *testing.T) {
	md := func(attempts string) json.RawMessage {
		return json.RawMessage(`{"attempts":` + attempts + `}`)
	}
	tests := []struct {
		name       string
		metadata   json.RawMessage
		resolvedTo string
		wantN      int
		wantFall   bool
	}{
		{"not reported", json.RawMessage(`{}`), "a/x-1", 0, false},
		{"same model, other providers", md(`[{"model":"a/x-1","status":429},{"model":"a/x-1","status":200}]`), "a/x-1", 2, false},
		{"fallback model answered", md(`[{"model":"a/x-1","status":502},{"model":"b/y-2","status":200}]`), "a/x-1", 2, true},
		{"no routing decision known", md(`[{"model":"b/y-2","status":200}]`), "", 1, false},
		{"junk", md(`"none"`), "a/x-1", 0, false},
		{"junk entries", md(`[null,{"model":7}]`), "a/x-1", 0, false},
	}
	for _, tc := range tests {
		n, fall := upstreamAttempts(tc.metadata, tc.resolvedTo)
		if tc.name == "junk entries" {
			// Unparseable lists are ignored whole rather than half-read.
			if fall {
				t.Errorf("%s: fallback reported from junk", tc.name)
			}
			continue
		}
		if n != tc.wantN || fall != tc.wantFall {
			t.Errorf("%s: got %d, %v; want %d, %v", tc.name, n, fall, tc.wantN, tc.wantFall)
		}
	}
}

// A 4xx is a result, and nothing retries it.
func TestErrorStatusIsAResultAndNotRetried(t *testing.T) {
	for _, status := range []int{404, 500} {
		s := newServer(t, status, `{"error":{"code":404,"message":"No models match your request and model restrictions"}}`)
		res, err := newChat(t, s, false).Run(context.Background(), spec(config.RouterAuto, nil))
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if res.Status != status || !strings.Contains(string(res.APIError), "No models match") {
			t.Errorf("status %d: result = %d %s", status, res.Status, res.APIError)
		}
		if n := s.hits.Load(); n != 1 || res.Exchange.Attempts != 1 {
			t.Errorf("status %d: %d requests reached the server, %d attempts; the SDK retry must be off", status, n, res.Exchange.Attempts)
		}
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	s := newServer(t, 200, okBody)
	tier := config.CostTier("low")
	res, err := newChat(t, s, true).Run(context.Background(), spec(config.RouterAuto, &config.Plugin{CostTier: &tier}))
	if !errors.Is(err, ErrDryRun) {
		t.Fatalf("err = %v, want ErrDryRun", err)
	}
	if s.hits.Load() != 0 {
		t.Fatal("dry run reached the server")
	}
	if !res.Exchange.DryRun || res.PluginID != "auto-router" || !strings.Contains(string(res.Exchange.RequestBody), `"cost_tier":"low"`) {
		t.Errorf("dry run did not render the payload: %s", res.Exchange.RequestBody)
	}
}

// The SDK reads these environment variables into its app identity globals.
// SDK v0.9.40 does not apply those globals to chat completions, but a later
// version could, so the property tested is the outcome: no identity header
// ever reaches the server, whether because the SDK omitted it or the guard
// blocked it.
func TestAppIdentityFromEnvNeverReachesServer(t *testing.T) {
	for _, env := range []string{"OPENROUTER_X_TITLE", "OPENROUTER_HTTP_REFERER"} {
		t.Run(env, func(t *testing.T) {
			s := newServer(t, 200, okBody)
			t.Setenv(config.APIKeyEnv, fakeKey)
			t.Setenv(env, "https://example.test/my-app")
			chat := NewChat(NewHTTPClient(false, DefaultGuards()), s.URL)
			_, err := chat.Run(context.Background(), spec(config.RouterAuto, nil))
			if s.hits.Load() == 0 {
				if err == nil {
					t.Fatal("nothing sent and no error")
				}
				return
			}
			for _, h := range appIdentityHeaders {
				if v := s.lastReq.Load().Header.Get(h); v != "" {
					t.Errorf("server received %s = %q", h, v)
				}
			}
		})
	}
}

// The guard itself, exercised without the SDK.
func TestGuardNoAppIdentity(t *testing.T) {
	for _, h := range append([]string{"x-title", "http-referer"}, appIdentityHeaders...) {
		t.Run(h, func(t *testing.T) {
			s := newServer(t, 200, okBody)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(h, "my-app")
			resp, err := NewHTTPClient(false, DefaultGuards()).Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, ErrAppIdentity) {
				t.Fatalf("err = %v, want ErrAppIdentity", err)
			}
			if s.hits.Load() != 0 {
				t.Fatal("request with an app identity header reached the server")
			}
		})
	}
}

func TestGuardPluginID(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{"auto matched", `{"model":"openrouter/auto","plugins":[{"id":"auto-router"}]}`, true},
		{"beta matched", `{"model":"openrouter/auto-beta","plugins":[{"id":"auto-beta-router"}]}`, true},
		{"auto with beta id", `{"model":"openrouter/auto","plugins":[{"id":"auto-beta-router"}]}`, false},
		{"beta with auto id", `{"model":"openrouter/auto-beta","plugins":[{"id":"auto-router"}]}`, false},
		{"router plugin on a pinned model", `{"model":"openai/gpt-5","plugins":[{"id":"auto-router"}]}`, false},
		{"matched plus mismatched", `{"model":"openrouter/auto","plugins":[{"id":"auto-router"},{"id":"auto-beta-router"}]}`, false},
		{"no plugins", `{"model":"openrouter/auto"}`, true},
		{"unrelated plugin", `{"model":"openrouter/auto","plugins":[{"id":"web"}]}`, true},
		{"not json", `model=openrouter/auto`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://example.test/chat/completions", nil)
			err := GuardPluginID(req, []byte(tc.body))
			if tc.ok && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected the guard to block")
			}
		})
	}
	get := httptest.NewRequest(http.MethodGet, "https://example.test/generation?id=x", nil)
	if err := GuardPluginID(get, nil); err != nil {
		t.Errorf("GET without a body blocked: %v", err)
	}
}

// The provider block must reach the wire field for field, with explicit
// false values kept and unset fields absent.
func TestProviderOnWire(t *testing.T) {
	tests := []struct {
		name string
		in   config.Provider
		want string // exact JSON of the provider object
	}{
		{"max price", config.Provider{MaxPrice: &config.MaxPrice{Prompt: ptr(0.0), Completion: ptr(2.5)}}, `{"max_price":{"completion":"2.5","prompt":"0"}}`},
		{"explicit false", config.Provider{AllowFallbacks: ptr(false), ZDR: ptr(false), RequireParameters: ptr(true)}, `{"allow_fallbacks":false,"require_parameters":true,"zdr":false}`},
		{
			"lists and enums",
			config.Provider{Order: []string{"together"}, Only: []string{"together", "deepinfra"}, Ignore: []string{"azure"}, Sort: ptr("price"), DataCollection: ptr("deny")},
			`{"data_collection":"deny","ignore":["azure"],"only":["together","deepinfra"],"order":["together"],"sort":"price"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, 200, okBody)
			sp := spec(config.RouterAuto, nil)
			sp.Provider = &tc.in
			res, err := newChat(t, s, false).Run(context.Background(), sp)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Provider json.RawMessage `json:"provider"`
			}
			if err := json.Unmarshal(res.Exchange.RequestBody, &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire.Provider) != tc.want {
				t.Errorf("provider on wire = %s\n want %s", wire.Provider, tc.want)
			}
		})
	}
	// No provider config, no provider key.
	s := newServer(t, 200, okBody)
	res, err := newChat(t, s, false).Run(context.Background(), spec(config.RouterAuto, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Exchange.RequestBody), `"provider"`) {
		t.Errorf("provider key sent with no provider config: %s", res.Exchange.RequestBody)
	}
}

func TestContextDeadlineApplies(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	t.Setenv(config.APIKeyEnv, fakeKey)
	chat := NewChat(NewHTTPClient(false, DefaultGuards()), slow.URL)
	sp := spec(config.RouterAuto, nil)
	sp.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := chat.Run(context.Background(), sp)
	if !errors.Is(err, ErrNoResponse) || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %v, want a prompt deadline error", err, time.Since(start))
	}
	sp.Timeout = 0
	if _, err := chat.Run(context.Background(), sp); err == nil {
		t.Fatal("a trial without a timeout was accepted")
	}
}
