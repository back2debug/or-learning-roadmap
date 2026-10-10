package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		router   Router
		slug     string
		pluginID string
	}{
		{RouterAuto, "openrouter/auto", "auto-router"},
		{RouterAutoBeta, "openrouter/auto-beta", "auto-beta-router"},
	}
	for _, tc := range tests {
		t.Run(string(tc.router), func(t *testing.T) {
			got, err := Resolve(tc.router)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.ModelSlug != tc.slug || got.PluginID != tc.pluginID || got.Router != tc.router {
				t.Errorf("Resolve(%q) = %+v, want slug %q plugin %q", tc.router, got, tc.slug, tc.pluginID)
			}
			// And back: the slug must lead to the same plugin id.
			id, ok := PluginIDForSlug(tc.slug)
			if !ok || id != tc.pluginID {
				t.Errorf("PluginIDForSlug(%q) = %q, %v; want %q", tc.slug, id, ok, tc.pluginID)
			}
			if !IsRouterPluginID(tc.pluginID) {
				t.Errorf("IsRouterPluginID(%q) = false", tc.pluginID)
			}
		})
	}
	if len(Routers()) != len(tests) {
		t.Errorf("Routers() has %d entries, table covers %d", len(Routers()), len(tests))
	}
}

func TestResolveUnknown(t *testing.T) {
	for _, r := range []Router{"", "both", "Auto", "auto-router", "openrouter/auto"} {
		if _, err := Resolve(r); !errors.Is(err, ErrUnknownRouter) {
			t.Errorf("Resolve(%q) error = %v, want ErrUnknownRouter", r, err)
		}
	}
	if _, ok := PluginIDForSlug("openai/gpt-5"); ok {
		t.Error("PluginIDForSlug matched a non-router slug")
	}
	if IsRouterPluginID("web") {
		t.Error("IsRouterPluginID matched a non-router plugin")
	}
}

// TestNoStrayRouterLiterals enforces that router.go is the only non-test file
// that spells out a model slug or plugin id, including through the SDK's
// generated constants.
func TestNoStrayRouterLiterals(t *testing.T) {
	banned := []string{`"auto-router"`, `"auto-beta-router"`, `"openrouter/auto`, "PluginIDAutoRouter", "PluginIDAutoBetaRouter"}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "router.go" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, b := range banned {
			if strings.Contains(string(src), b) {
				t.Errorf("%s contains %s; use config.Resolve instead", path, b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
