package config

import (
	"os"
	"strings"
)

// ProviderAPIKeyEnv returns the conventional API-key env var for a
// built-in provider name. Empty means "no API key expected by default".
func ProviderAPIKeyEnv(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "google", "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	case "mistral":
		return "MISTRAL_API_KEY"
	case "cerebras":
		return "CEREBRAS_API_KEY"
	case "litellm":
		return "LITELLM_API_KEY"
	default:
		return ""
	}
}

// ResolveProviderAPIKey returns the configured API key from the
// conventional env var for the given provider name.
func ResolveProviderAPIKey(provider string) string {
	if env := ProviderAPIKeyEnv(provider); env != "" {
		return os.Getenv(env)
	}
	return ""
}

// BuiltinInferencePreset returns the endpoint and conventional API-key env var
// for stado's bundled OAI-compatible provider names.
func BuiltinInferencePreset(name string) (endpoint, apiKeyEnv string, ok bool) {
	switch strings.ToLower(name) {
	case "ollama":
		return "http://localhost:11434/v1", "", true
	case "ollama-cloud":
		return "https://ollama.com/v1", "OLLAMA_CLOUD_API_KEY", true
	case "llamacpp":
		return "http://localhost:8080/v1", "", true
	case "vllm":
		return "http://localhost:8000/v1", "", true
	case "lmstudio":
		return "http://localhost:1234/v1", "", true
	case "litellm":
		return "http://localhost:4000/v1", ProviderAPIKeyEnv(name), true
	case "groq":
		return "https://api.groq.com/openai/v1", ProviderAPIKeyEnv(name), true
	case "openrouter":
		return "https://openrouter.ai/api/v1", ProviderAPIKeyEnv(name), true
	case "deepseek":
		return "https://api.deepseek.com/v1", ProviderAPIKeyEnv(name), true
	case "xai":
		return "https://api.x.ai/v1", ProviderAPIKeyEnv(name), true
	case "mistral":
		return "https://api.mistral.ai/v1", ProviderAPIKeyEnv(name), true
	case "cerebras":
		return "https://api.cerebras.ai/v1", ProviderAPIKeyEnv(name), true
	default:
		return "", "", false
	}
}

// PresetAPIKeyEnv reports the env var stado will read for an OAI-compat
// preset, in priority order:
//
//  1. preset.APIKeyEnv when set (explicit user opt-in).
//  2. The builtin convention env var when name matches a bundled preset.
//  3. STADO_PRESET_<UPPER>_API_KEY as a default for custom preset names,
//     with hyphens normalized to underscores so `ollama-cloud` maps to
//     `STADO_PRESET_OLLAMA_CLOUD_API_KEY`.
//
// Returns "" only when nothing matches — e.g. a builtin local-runner
// preset that doesn't expect a key.
func PresetAPIKeyEnv(name string, preset InferencePreset) string {
	if env := strings.TrimSpace(preset.APIKeyEnv); env != "" {
		return env
	}
	if _, env, ok := BuiltinInferencePreset(name); ok {
		return env
	}
	norm := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(name)), "-", "_")
	if norm == "" {
		return ""
	}
	return "STADO_PRESET_" + norm + "_API_KEY"
}

// ResolvePresetAPIKey returns the configured API key for the given
// preset by consulting PresetAPIKeyEnv. Empty means "no credentials
// found" — caller decides whether that's fatal.
func ResolvePresetAPIKey(name string, preset InferencePreset) string {
	env := PresetAPIKeyEnv(name, preset)
	if env == "" {
		return ""
	}
	return os.Getenv(env)
}
