// Package config resolves flags and environment into a validated run
// configuration. The API key comes from the environment only — never a flag,
// never a file.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// EnvAPIKey is the name of the variable the key is read from — the only
	// place orlab accepts it. The name is not itself a secret.
	EnvAPIKey      = "OPENROUTER_API_KEY"           // #nosec G101 -- env var name, not a credential
	DefaultBaseURL = "https://openrouter.ai/api/v1" // #nosec G101 -- public API endpoint
	// keyPrefix is the public prefix every OpenRouter key carries, used to
	// reject OpenAI keys early. Not a credential.
	keyPrefix = "sk-or-" // #nosec G101 -- public key prefix, not a secret
)

type Config struct {
	APIKey  string // validated sk-or- key; empty in --replay and --dry-run modes
	BaseURL string

	SuitePath string   // "" means the embedded default suite
	Models    []string // slugs to run; validated at preflight against /models
	Cheap     bool     // swap in :free / lowest-cost variants

	Stream       bool // echo_upstream_body requires streaming; default true
	EchoUpstream bool
	DryRun       bool
	ReplayDir    string
	Repeat       int

	OutDir      string
	Concurrency int
	Timeout     time.Duration // per-request; the run deadline derives from this

	AllowInsecureLocalhost bool
}

// Parse reads flags from args (not including the program name) and the
// environment. It returns flag.ErrHelp for -h.
func Parse(args []string, getenv func(string) string) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("orlab", flag.ContinueOnError)

	var models string
	var noStream bool
	fs.StringVar(&cfg.BaseURL, "base-url", DefaultBaseURL, "OpenRouter API base URL (https required unless -allow-insecure-localhost)")
	fs.StringVar(&cfg.SuitePath, "suite", "", "path to a YAML/JSON prompt suite (default: embedded suite)")
	fs.StringVar(&models, "models", "", "comma-separated model slugs (default: built-in list)")
	fs.BoolVar(&cfg.Cheap, "cheap", false, "swap models for :free/lowest-cost variants")
	fs.BoolVar(&noStream, "no-stream", false, "run buffered instead of streamed (echo data will be absent)")
	fs.BoolVar(&cfg.EchoUpstream, "echo-upstream", true, "request debug.echo_upstream_body (streaming only)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "build and diff request bodies, send nothing")
	fs.StringVar(&cfg.ReplayDir, "replay", "", "replay recorded cassettes from this directory instead of the network")
	fs.IntVar(&cfg.Repeat, "repeat", 1, "repetitions per suite entry (determinism probe)")
	fs.StringVar(&cfg.OutDir, "out", "runs", "directory for run records and reports")
	fs.IntVar(&cfg.Concurrency, "concurrency", 2, "max requests in flight (never more than one per model)")
	fs.DurationVar(&cfg.Timeout, "timeout", 120*time.Second, "per-request timeout")
	fs.BoolVar(&cfg.AllowInsecureLocalhost, "allow-insecure-localhost", false, "permit http:// base URLs that resolve to loopback")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg.Stream = !noStream
	if models != "" {
		for _, m := range strings.Split(models, ",") {
			if m = strings.TrimSpace(m); m != "" {
				cfg.Models = append(cfg.Models, m)
			}
		}
	}

	cfg.APIKey = getenv(EnvAPIKey)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	offline := c.DryRun || c.ReplayDir != ""
	switch {
	case c.APIKey == "" && !offline:
		return fmt.Errorf("%s is not set (use -dry-run or -replay to run without a key)", EnvAPIKey)
	case c.APIKey != "" && !strings.HasPrefix(c.APIKey, keyPrefix):
		if strings.HasPrefix(c.APIKey, "sk-") {
			return errors.New("API key looks like an OpenAI key (sk-…); OpenRouter keys begin sk-or-")
		}
		return errors.New("API key must begin with sk-or-")
	}

	if err := c.validateBaseURL(); err != nil {
		return err
	}
	if c.Concurrency < 1 {
		return errors.New("-concurrency must be >= 1")
	}
	if c.Repeat < 1 {
		return errors.New("-repeat must be >= 1")
	}
	if c.Timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	return nil
}

func (c *Config) validateBaseURL() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid -base-url: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !c.AllowInsecureLocalhost {
			return errors.New("-base-url must be https:// (or pass -allow-insecure-localhost for a loopback mock)")
		}
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("-allow-insecure-localhost only permits loopback hosts, got %q", u.Hostname())
		}
		return nil
	default:
		return fmt.Errorf("unsupported -base-url scheme %q", u.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !a.IsLoopback() {
			return false
		}
	}
	return true
}

// Getenv is the production environment source.
func Getenv(key string) string { return os.Getenv(key) }
