package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/localdetect"
)

func TestPickLocalFallbackPrefersLoadedModels(t *testing.T) {
	results := []localdetect.Result{
		{Name: "ollama", Endpoint: "http://localhost:11434/v1", Reachable: true},
		{Name: "lmstudio", Endpoint: "http://localhost:1234/v1", Reachable: true, Models: []string{"qwen/qwen3.6-35b-a3b"}},
	}

	got := pickLocalFallback(results)
	if got == nil {
		t.Fatal("pickLocalFallback returned nil")
	}
	if got.Name != "lmstudio" {
		t.Fatalf("pickLocalFallback chose %q, want lmstudio", got.Name)
	}
}

func TestPickLocalFallbackFallsBackToFirstReachable(t *testing.T) {
	results := []localdetect.Result{
		{Name: "ollama", Endpoint: "http://localhost:11434/v1", Reachable: true},
		{Name: "lmstudio", Endpoint: "http://localhost:1234/v1", Reachable: true},
	}

	got := pickLocalFallback(results)
	if got == nil {
		t.Fatal("pickLocalFallback returned nil")
	}
	if got.Name != "ollama" {
		t.Fatalf("pickLocalFallback chose %q, want ollama", got.Name)
	}
}

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
	if prov == nil {
		t.Fatal("buildProvider returned nil")
	}
	if !strings.Contains(prov.Name(), "lmstudio") {
		t.Errorf("provider Name = %q, want to contain 'lmstudio'", prov.Name())
	}
}
