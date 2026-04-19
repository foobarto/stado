package tui

import "testing"

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
