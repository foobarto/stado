package config

import (
	"testing"
)

func TestProviderAPIKeyEnv(t *testing.T) {
	cases := map[string]string{
		"anthropic":  "ANTHROPIC_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"google":     "GEMINI_API_KEY",
		"gemini":     "GEMINI_API_KEY",
		"groq":       "GROQ_API_KEY",
		"openrouter": "OPENROUTER_API_KEY",
		"deepseek":   "DEEPSEEK_API_KEY",
		"xai":        "XAI_API_KEY",
		"mistral":    "MISTRAL_API_KEY",
		"cerebras":   "CEREBRAS_API_KEY",
		"litellm":    "LITELLM_API_KEY",
		"ollama":     "",
		"unknown":    "",
	}
	for provider, want := range cases {
		if got := ProviderAPIKeyEnv(provider); got != want {
			t.Errorf("ProviderAPIKeyEnv(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestBuiltinInferencePreset(t *testing.T) {
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
		ep, keyEnv, ok := BuiltinInferencePreset(c.name)
		if !ok {
			t.Errorf("BuiltinInferencePreset(%q) not found", c.name)
			continue
		}
		if c.wantHTTPS && ep[:8] != "https://" {
			t.Errorf("BuiltinInferencePreset(%q).endpoint = %q, want https://", c.name, ep)
		}
		if !c.wantHTTPS && ep[:7] != "http://" {
			t.Errorf("BuiltinInferencePreset(%q).endpoint = %q, want http://", c.name, ep)
		}
		if keyEnv != c.wantEnv {
			t.Errorf("BuiltinInferencePreset(%q).env = %q, want %q", c.name, keyEnv, c.wantEnv)
		}
	}
	if _, _, ok := BuiltinInferencePreset("not-a-real-provider-xyzzy"); ok {
		t.Error("unknown provider should return ok=false")
	}
}

func TestBuiltinInferencePresetOllamaCloud(t *testing.T) {
	ep, env, ok := BuiltinInferencePreset("ollama-cloud")
	if !ok {
		t.Fatal("ollama-cloud should be a builtin preset")
	}
	if ep != "https://ollama.com/v1" {
		t.Errorf("ollama-cloud endpoint = %q, want https://ollama.com/v1", ep)
	}
	if env != "OLLAMA_CLOUD_API_KEY" {
		t.Errorf("ollama-cloud env = %q, want OLLAMA_CLOUD_API_KEY", env)
	}
}

func TestPresetAPIKeyEnv(t *testing.T) {
	cases := []struct {
		desc   string
		name   string
		preset InferencePreset
		want   string
	}{
		{"explicit field wins for unknown name",
			"custom-thing", InferencePreset{APIKeyEnv: "MY_KEY"}, "MY_KEY"},
		{"explicit field beats builtin convention",
			"litellm", InferencePreset{APIKeyEnv: "OVERRIDE_KEY"}, "OVERRIDE_KEY"},
		{"builtin preset uses convention env",
			"litellm", InferencePreset{}, "LITELLM_API_KEY"},
		{"ollama-cloud builtin preset",
			"ollama-cloud", InferencePreset{}, "OLLAMA_CLOUD_API_KEY"},
		{"unknown preset falls back to STADO_PRESET_<UPPER>_API_KEY",
			"ollama-cloud-byo", InferencePreset{}, "STADO_PRESET_OLLAMA_CLOUD_BYO_API_KEY"},
		{"local-only builtin returns empty",
			"ollama", InferencePreset{}, ""},
		{"trims whitespace in field",
			"anything", InferencePreset{APIKeyEnv: "  TRIMMED_KEY  "}, "TRIMMED_KEY"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := PresetAPIKeyEnv(c.name, c.preset)
			if got != c.want {
				t.Errorf("PresetAPIKeyEnv(%q, %+v) = %q, want %q", c.name, c.preset, got, c.want)
			}
		})
	}
}

func TestResolvePresetAPIKey_ReadsEnv(t *testing.T) {
	t.Setenv("STADO_PRESET_OLLAMA_CLOUD_BYO_API_KEY", "abc123")
	got := ResolvePresetAPIKey("ollama-cloud-byo", InferencePreset{})
	if got != "abc123" {
		t.Errorf("ResolvePresetAPIKey custom = %q, want abc123", got)
	}
	t.Setenv("MY_PINNED_ENV", "pinned-key")
	got = ResolvePresetAPIKey("anything", InferencePreset{APIKeyEnv: "MY_PINNED_ENV"})
	if got != "pinned-key" {
		t.Errorf("ResolvePresetAPIKey pinned = %q, want pinned-key", got)
	}
	got = ResolvePresetAPIKey("ollama", InferencePreset{})
	if got != "" {
		t.Errorf("ResolvePresetAPIKey ollama = %q, want empty (no key env defined)", got)
	}
}
