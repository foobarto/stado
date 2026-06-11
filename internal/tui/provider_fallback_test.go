package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/anthropic"
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

// Regression guard: selecting a detected local fallback must not print
// directly to stderr while the TUI is running, because raw writes under
// alt-screen corrupt the layout.
func TestBuildProvider_EmptyProbesLocal_DoesNotWriteStderr(t *testing.T) {
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

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	_, err = buildProviderByName(cfg, "")
	_ = w.Close()
	if err != nil {
		t.Fatalf("buildProviderByName(\"\"): %v", err)
	}
	got, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("read stderr capture: %v", readErr)
	}
	if len(strings.TrimSpace(string(got))) != 0 {
		t.Fatalf("buildProviderByName wrote to stderr: %q", string(got))
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

// TestBuildProvider_MinimaxAnthropic — the bundled minimax-anthropic
// name resolves to the native anthropic SDK pointed at MiniMax's
// Claude-compatible endpoint, reporting the custom name (not bare
// "anthropic"). With MINIMAX_API_KEY set it builds; without it, errors
// naming the right env var (proving it didn't fall through to the
// OAI-compat path, which would have built a no-key oaicompat provider).
func TestBuildProvider_MinimaxAnthropic(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Provider: "minimax-anthropic"}}

	t.Setenv("MINIMAX_API_KEY", "")
	if _, err := buildProviderByName(cfg, "minimax-anthropic"); err == nil ||
		!strings.Contains(err.Error(), "MINIMAX_API_KEY") {
		t.Fatalf("want error naming MINIMAX_API_KEY, got %v", err)
	}

	t.Setenv("MINIMAX_API_KEY", "mm-fake-test-key")
	p, err := buildProviderByName(cfg, "minimax-anthropic")
	if err != nil {
		t.Fatalf("buildProviderByName(minimax-anthropic): %v", err)
	}
	if p.Name() != "minimax-anthropic" {
		t.Errorf("expected minimax-anthropic, got %q", p.Name())
	}
	// No preset override → the bundled registry endpoint is used.
	ap, ok := p.(*anthropic.Provider)
	if !ok {
		t.Fatalf("expected *anthropic.Provider, got %T", p)
	}
	if ap.BaseURL() != "https://api.minimax.io/anthropic" {
		t.Errorf("BaseURL = %q, want the bundled minimax-anthropic endpoint", ap.BaseURL())
	}
}

// TestBuildProvider_AnthropicCompatBaseURLOverride — a base_url written by
// `stado auth set minimax-anthropic --base-url ...` lands in the
// [inference.presets.minimax-anthropic] block and must reach the anthropic
// SDK via WithBaseURL, overriding the bundled registry endpoint.
func TestBuildProvider_AnthropicCompatBaseURLOverride(t *testing.T) {
	t.Setenv("MINIMAX_API_KEY", "mm-fake-test-key")
	cfg := &config.Config{Defaults: config.Defaults{Provider: "minimax-anthropic"}}
	cfg.Inference.Presets = map[string]config.InferencePreset{
		"minimax-anthropic": {BaseURL: "https://proxy.internal/anthropic"},
	}

	p, err := buildProviderByName(cfg, "minimax-anthropic")
	if err != nil {
		t.Fatalf("buildProviderByName(minimax-anthropic): %v", err)
	}
	ap, ok := p.(*anthropic.Provider)
	if !ok {
		t.Fatalf("expected *anthropic.Provider, got %T", p)
	}
	if got := ap.BaseURL(); got != "https://proxy.internal/anthropic" {
		t.Errorf("BaseURL = %q, want the preset override https://proxy.internal/anthropic", got)
	}
}

// TestBuildProvider_AnthropicCompatAPIKeyEnvOverride — an api_key_env
// override (written by `auth set --env`) reroutes the key lookup to a
// non-conventional env var for an anthropic-compat-cloud provider.
func TestBuildProvider_AnthropicCompatAPIKeyEnvOverride(t *testing.T) {
	t.Setenv("MINIMAX_API_KEY", "")                 // conventional var empty
	t.Setenv("CUSTOM_MM_KEY", "mm-from-custom-env") // override holds the key
	cfg := &config.Config{Defaults: config.Defaults{Provider: "minimax-anthropic"}}
	cfg.Inference.Presets = map[string]config.InferencePreset{
		"minimax-anthropic": {APIKeyEnv: "CUSTOM_MM_KEY"},
	}

	p, err := buildProviderByName(cfg, "minimax-anthropic")
	if err != nil {
		t.Fatalf("buildProviderByName should resolve via CUSTOM_MM_KEY: %v", err)
	}
	if p.Name() != "minimax-anthropic" {
		t.Errorf("expected minimax-anthropic, got %q", p.Name())
	}
}
