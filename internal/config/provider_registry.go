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
