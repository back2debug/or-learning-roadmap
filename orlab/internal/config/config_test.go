package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

const goodKey = "sk-or-v1-abc123"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, c *Config)
	}{
		{
			name: "defaults with key",
			env:  map[string]string{EnvAPIKey: goodKey},
			check: func(t *testing.T, c *Config) {
				if !c.Stream || !c.EchoUpstream || c.Concurrency != 2 || c.BaseURL != DefaultBaseURL {
					t.Errorf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name:    "missing key online",
			wantErr: "OPENROUTER_API_KEY is not set",
		},
		{
			name: "missing key ok in dry-run",
			args: []string{"-dry-run"},
		},
		{
			name: "missing key ok in replay",
			args: []string{"-replay", "testdata/cassettes"},
		},
		{
			name:    "openai key rejected with hint",
			env:     map[string]string{EnvAPIKey: "sk-abc"},
			wantErr: "looks like an OpenAI key",
		},
		{
			name:    "garbage key rejected",
			env:     map[string]string{EnvAPIKey: "hunter2"},
			wantErr: "must begin with sk-or-",
		},
		{
			name:    "wrong key rejected even in dry-run",
			args:    []string{"-dry-run"},
			env:     map[string]string{EnvAPIKey: "sk-abc"},
			wantErr: "looks like an OpenAI key",
		},
		{
			name:    "http base url rejected",
			args:    []string{"-base-url", "http://openrouter.ai/api/v1"},
			env:     map[string]string{EnvAPIKey: goodKey},
			wantErr: "must be https://",
		},
		{
			name: "http loopback allowed with flag",
			args: []string{"-base-url", "http://127.0.0.1:8080", "-allow-insecure-localhost"},
			env:  map[string]string{EnvAPIKey: goodKey},
		},
		{
			name:    "http non-loopback rejected even with flag",
			args:    []string{"-base-url", "http://openrouter.ai", "-allow-insecure-localhost"},
			env:     map[string]string{EnvAPIKey: goodKey},
			wantErr: "only permits loopback",
		},
		{
			name: "no-stream flips stream",
			args: []string{"-no-stream"},
			env:  map[string]string{EnvAPIKey: goodKey},
			check: func(t *testing.T, c *Config) {
				if c.Stream {
					t.Error("Stream = true with -no-stream")
				}
			},
		},
		{
			name: "models parsed and trimmed",
			args: []string{"-models", " x-ai/grok-4.6, google/gemini-2.5-flash ,"},
			env:  map[string]string{EnvAPIKey: goodKey},
			check: func(t *testing.T, c *Config) {
				if len(c.Models) != 2 || c.Models[0] != "x-ai/grok-4.6" || c.Models[1] != "google/gemini-2.5-flash" {
					t.Errorf("Models = %v", c.Models)
				}
			},
		},
		{
			name:    "bad concurrency",
			args:    []string{"-concurrency", "0"},
			env:     map[string]string{EnvAPIKey: goodKey},
			wantErr: "-concurrency",
		},
		{
			name:    "bad repeat",
			args:    []string{"-repeat", "0"},
			env:     map[string]string{EnvAPIKey: goodKey},
			wantErr: "-repeat",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(tt.args, env(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}
