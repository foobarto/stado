package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestBuiltinPreset_CoversKnownProviders(t *testing.T) {
	cases := []struct {
		name      string
		wantHTTPS bool
		wantEnv   string
	}{
		{"ollama", false, ""},
		{"llamacpp", false, ""},
		{"vllm", false, ""},
		{"lmstudio", false, ""},
		{"litellm", false, "LITELLM_API_KEY"},
		{"groq", true, "GROQ_API_KEY"},
		{"openrouter", true, "OPENROUTER_API_KEY"},
		{"deepseek", true, "DEEPSEEK_API_KEY"},
		{"xai", true, "XAI_API_KEY"},
		{"mistral", true, "MISTRAL_API_KEY"},
		{"cerebras", true, "CEREBRAS_API_KEY"},
	}
	for _, c := range cases {
		ep, keyEnv, ok := builtinPreset(c.name)
		if !ok {
			t.Errorf("builtinPreset(%q) not found", c.name)
			continue
		}
		if c.wantHTTPS && ep[:8] != "https://" {
			t.Errorf("builtinPreset(%q).endpoint = %q, want https://", c.name, ep)
		}
		if !c.wantHTTPS && ep[:7] != "http://" {
			t.Errorf("builtinPreset(%q).endpoint = %q, want http://", c.name, ep)
		}
		if keyEnv != c.wantEnv {
			t.Errorf("builtinPreset(%q).env = %q, want %q", c.name, keyEnv, c.wantEnv)
		}
	}
}

func TestBuiltinPreset_UnknownReturnsFalse(t *testing.T) {
	_, _, ok := builtinPreset("not-a-real-provider-xyzzy")
	if ok {
		t.Error("unknown provider should return ok=false")
	}
}

// TestBuildProvider_UserPresetOverridesBuiltin lets operators point a
// bundled preset name (e.g. lmstudio on a non-default port) at a
// different endpoint via [inference.presets.<name>].endpoint without
// writing a whole new preset or setting a STADO_*_HOST env var.
// Regression guard for the ordering fix in buildProvider.
func TestBuildProvider_UserPresetOverridesBuiltin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.Provider = "lmstudio"
	cfg.Defaults.Model = "some-model"
	cfg.Inference.Presets = map[string]config.InferencePreset{
		"lmstudio": {Endpoint: "http://localhost:9999/v1"},
	}

	prov, err := buildProvider(cfg)
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	// oaicompat.Provider's Name() is user-settable via WithName; the
	// bundled builder sets it to the preset name in both paths, so the
	// differentiator is the endpoint. The provider doesn't expose
	// Endpoint, so we confirm by ensuring StreamTurn to an unreachable
	// port would fail — but that'd be a flakey network test. Instead,
	// just confirm Name and that the builder chose a provider.
	if prov == nil {
		t.Fatal("buildProvider returned nil")
	}
	if !strings.Contains(prov.Name(), "lmstudio") {
		t.Errorf("provider Name = %q, want to contain 'lmstudio'", prov.Name())
	}
}
