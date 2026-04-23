package config

import "testing"

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
