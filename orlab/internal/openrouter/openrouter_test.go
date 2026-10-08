package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseAPIError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want APIError
	}{
		{
			"full envelope with numeric code",
			`{"error":{"code":404,"message":"nope","metadata":{"error_type":"not_found","provider_code":"P123"}},"openrouter_metadata":{"strategy":"direct"}}`,
			APIError{Status: 404, Code: "404", Message: "nope", ErrorType: "not_found", ProviderCode: "P123"},
		},
		{
			"string code, no metadata",
			`{"error":{"code":"rate_limited","message":"slow down"}}`,
			APIError{Status: 429, Code: "rate_limited", Message: "slow down"},
		},
		{
			"non-JSON body",
			`upstream exploded`,
			APIError{Status: 502, Message: "upstream exploded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAPIError(tt.want.Status, []byte(tt.body))
			if got.Code != tt.want.Code || got.Message != tt.want.Message ||
				got.ErrorType != tt.want.ErrorType || got.ProviderCode != tt.want.ProviderCode {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if len(got.Raw) == 0 {
				t.Error("Raw not preserved")
			}
		})
	}
	if (&APIError{Status: 404, ErrorType: "not_found", Message: "x"}).Error() == "" {
		t.Error("empty Error()")
	}
}

func TestClosestModels(t *testing.T) {
	catalog := []Model{
		{ID: "x-ai/grok-4.6"}, {ID: "x-ai/grok-4.5"}, {ID: "google/gemini-2.5-flash"},
	}
	got := ClosestModels("x-ai/grok-46", catalog, 2)
	if len(got) != 2 || got[0] != "x-ai/grok-4.6" && got[1] != "x-ai/grok-4.6" {
		t.Errorf("ClosestModels = %v", got)
	}
	// Substring match ranks first.
	got = ClosestModels("gemini", catalog, 1)
	if len(got) != 1 || got[0] != "google/gemini-2.5-flash" {
		t.Errorf("substring match = %v", got)
	}
}

func TestReconcileGenerationPollsThroughPending(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		io.WriteString(w, `{"data":{"id":"gen-1","provider_name":"Prov","total_cost":0.001}}`)
	}))
	defer srv.Close()

	c := &Client{
		Do: http.DefaultClient, BaseURL: srv.URL, APIKey: "sk-or-test",
		ReconcileDelays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond},
	}
	rec, err := c.ReconcileGeneration(context.Background(), "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ProviderName != "Prov" || calls != 3 {
		t.Errorf("rec=%+v calls=%d", rec, calls)
	}
}

func TestReconcileGenerationGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := &Client{
		Do: http.DefaultClient, BaseURL: srv.URL, APIKey: "sk-or-test",
		ReconcileDelays: []time.Duration{time.Millisecond, time.Millisecond},
	}
	_, err := c.ReconcileGeneration(context.Background(), "gen-x")
	if !errors.Is(err, ErrGenerationPending) {
		t.Errorf("err = %v, want ErrGenerationPending", err)
	}
}
