package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestBuildProvider_NoFallbackWhenAPIKeySet — if ANTHROPIC_API_KEY is
// present, the anthropic path runs unchanged. No probe, no swap.
func TestBuildProvider_NoFallbackWhenAPIKeySet(t *testing.T) {
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

// TestBuildProvider_FallsBackToLocalWhenNoKey — the bug that triggered
// this work: `stado run ...` with no ANTHROPIC_API_KEY should swap to
// a detected localhost OAI-compat runner instead of failing hard.
//
// Fake a local OAI-compat server with a working /v1/models endpoint,
// register it as a user preset, and assert buildProviderByName picks
// it up via the fallback path.
func TestBuildProvider_FallsBackToLocalWhenNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := &config.Config{
		Defaults: config.Defaults{Provider: "anthropic"},
		Inference: config.Inference{
			Presets: map[string]config.InferencePreset{
				"testlocal": {Endpoint: srv.URL + "/v1"},
			},
		},
	}

	p, err := buildProviderByName(cfg, "anthropic")
	if err != nil {
		t.Fatalf("buildProviderByName: %v", err)
	}
	if p == nil {
		t.Fatal("expected fallback provider, got nil")
	}
	if p.Name() == "anthropic" {
		t.Errorf("expected local fallback, got anthropic — fallback did not trigger")
	}
}

// TestBuildProvider_ExplicitNonAnthropicSkipsFallback — a user who
// explicitly configured a non-anthropic provider doesn't want the
// fallback. Only the default "anthropic" + no-key case triggers it.
func TestBuildProvider_ExplicitNonAnthropicSkipsFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := &config.Config{Defaults: config.Defaults{Provider: "ollama"}}
	// buildProviderByName("ollama") goes through builtinPreset; it
	// returns an oaicompat provider without any localdetect probe.
	p, err := buildProviderByName(cfg, "ollama")
	if err != nil {
		t.Fatalf("buildProviderByName: %v", err)
	}
	if p.Name() != "ollama" {
		t.Errorf("expected ollama provider, got %q", p.Name())
	}
}
