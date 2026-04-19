package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestBuildProvider_EmptyProbesLocal: the no-default-configured case
// is the primary trigger for local auto-detection. A preset pointed
// at an httptest server with a working /v1/models endpoint should
// win the race.
func TestBuildProvider_EmptyProbesLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Inference: config.Inference{
			Presets: map[string]config.InferencePreset{
				"testlocal": {Endpoint: srv.URL + "/v1"},
			},
		},
	}

	p, err := buildProviderByName(cfg, "")
	if err != nil {
		t.Fatalf("buildProviderByName(\"\"): %v", err)
	}
	if p == nil {
		t.Fatal("expected fallback provider, got nil")
	}
}

// TestBuildProvider_EmptyWithNoLocalErrors: no config + no local
// inference runner on any probed endpoint yields a clear error — not
// a blank panic or a misleading anthropic-specific one.
func TestBuildProvider_EmptyWithNoLocalErrors(t *testing.T) {
	// Construct a config with a preset pointing at a definitely-dead
	// endpoint so even the user-preset path can't trigger the
	// fallback. Bundled localhost endpoints (ollama/lmstudio/...) may
	// or may not be up on the test host; this test asserts that the
	// error path itself is well-formed, not the reachability result.
	cfg := &config.Config{
		Inference: config.Inference{
			Presets: map[string]config.InferencePreset{
				"dead": {Endpoint: "http://127.0.0.1:1/v1"},
			},
		},
	}

	_, err := buildProviderByName(cfg, "")
	if err == nil {
		return // A local runner is up on this host — not a failure, just can't test the error path here.
	}
	if !strings.Contains(err.Error(), "no provider configured") {
		t.Errorf("error should mention no-provider-configured, got: %v", err)
	}
	if !strings.Contains(err.Error(), "defaults.provider") {
		t.Errorf("error should point at defaults.provider config, got: %v", err)
	}
}

// TestBuildProvider_ExplicitAnthropicNotProbed: when the user sets
// defaults.provider = "anthropic" explicitly, we respect that —
// no local probe — and let anthropic.New handle the API-key check.
func TestBuildProvider_ExplicitAnthropicNotProbed(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic-fake-test-key")
	cfg := &config.Config{Defaults: config.Defaults{Provider: "anthropic"}}
	p, err := buildProviderByName(cfg, "anthropic")
	if err != nil {
		t.Fatalf("buildProviderByName: %v", err)
	}
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic, got %q", p.Name())
	}
}

// TestBuildProvider_ExplicitNonAnthropicSkipsFallback — a user who
// explicitly configured a non-anthropic provider doesn't want the
// fallback. Only the empty-provider case triggers the probe.
func TestBuildProvider_ExplicitNonAnthropicSkipsFallback(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Provider: "ollama"}}
	p, err := buildProviderByName(cfg, "ollama")
	if err != nil {
		t.Fatalf("buildProviderByName: %v", err)
	}
	if p.Name() != "ollama" {
		t.Errorf("expected ollama provider, got %q", p.Name())
	}
}
