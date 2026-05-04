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

// ProviderKind classifies a known provider by how stado talks to it.
type ProviderKind int

const (
	// ProviderKindNative — stado uses the vendor's first-party SDK
	// (anthropic-sdk-go, openai-go, generative-ai-go). No
	// [inference.presets.X] block is needed; setting the conventional
	// API-key env var is enough.
	ProviderKindNative ProviderKind = iota
	// ProviderKindOAICompatCloud — hosted endpoint speaking
	// OpenAI-compatible JSON. Needs an [inference.presets.X] block
	// (or matches a builtin name) plus the configured API key.
	ProviderKindOAICompatCloud
	// ProviderKindOAICompatLocal — local-runner endpoint speaking
	// OpenAI-compatible JSON (Ollama, llama.cpp, vLLM, LMStudio).
	// Usually needs no API key; just confirm the endpoint is reachable.
	ProviderKindOAICompatLocal
)

func (k ProviderKind) String() string {
	switch k {
	case ProviderKindNative:
		return "native"
	case ProviderKindOAICompatCloud:
		return "oai-compat-cloud"
	case ProviderKindOAICompatLocal:
		return "oai-compat-local"
	default:
		return "unknown"
	}
}

// KnownProvider is one entry in the bundled provider registry. UI and
// CLI surfaces should iterate KnownProviders() rather than hard-coding
// names so adding a new provider means touching one place.
type KnownProvider struct {
	Name      string
	Kind      ProviderKind
	Endpoint  string // empty for native providers
	APIKeyEnv string // empty for local-runner OAI-compat presets
	// HelpURL is a short hint for where to get an API key for this
	// provider. Optional.
	HelpURL string
}

// KnownProviders returns the bundled provider catalogue in display
// order: native first (most-used), then OAI-compat cloud (alpha),
// then OAI-compat local. Source of truth for `stado config
// providers` listings, doctor output, and any future TUI picker.
func KnownProviders() []KnownProvider {
	return []KnownProvider{
		// Native — first-party SDK
		{Name: "anthropic", Kind: ProviderKindNative, APIKeyEnv: "ANTHROPIC_API_KEY", HelpURL: "https://console.anthropic.com/"},
		{Name: "openai", Kind: ProviderKindNative, APIKeyEnv: "OPENAI_API_KEY", HelpURL: "https://platform.openai.com/api-keys"},
		{Name: "google", Kind: ProviderKindNative, APIKeyEnv: "GEMINI_API_KEY", HelpURL: "https://aistudio.google.com/apikey"},

		// OAI-compat cloud (alpha by name)
		{Name: "cerebras", Kind: ProviderKindOAICompatCloud, Endpoint: "https://api.cerebras.ai/v1", APIKeyEnv: "CEREBRAS_API_KEY", HelpURL: "https://cloud.cerebras.ai/platform/"},
		{Name: "deepseek", Kind: ProviderKindOAICompatCloud, Endpoint: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY", HelpURL: "https://platform.deepseek.com/api_keys"},
		{Name: "groq", Kind: ProviderKindOAICompatCloud, Endpoint: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY", HelpURL: "https://console.groq.com/keys"},
		{Name: "litellm", Kind: ProviderKindOAICompatCloud, Endpoint: "http://localhost:4000/v1", APIKeyEnv: "LITELLM_API_KEY", HelpURL: "https://docs.litellm.ai/"},
		{Name: "mistral", Kind: ProviderKindOAICompatCloud, Endpoint: "https://api.mistral.ai/v1", APIKeyEnv: "MISTRAL_API_KEY", HelpURL: "https://console.mistral.ai/api-keys"},
		{Name: "ollama-cloud", Kind: ProviderKindOAICompatCloud, Endpoint: "https://ollama.com/v1", APIKeyEnv: "OLLAMA_CLOUD_API_KEY", HelpURL: "https://ollama.com/settings/keys"},
		{Name: "openrouter", Kind: ProviderKindOAICompatCloud, Endpoint: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", HelpURL: "https://openrouter.ai/keys"},
		{Name: "xai", Kind: ProviderKindOAICompatCloud, Endpoint: "https://api.x.ai/v1", APIKeyEnv: "XAI_API_KEY", HelpURL: "https://console.x.ai/"},

		// OAI-compat local — no key needed
		{Name: "llamacpp", Kind: ProviderKindOAICompatLocal, Endpoint: "http://localhost:8080/v1"},
		{Name: "lmstudio", Kind: ProviderKindOAICompatLocal, Endpoint: "http://localhost:1234/v1"},
		{Name: "ollama", Kind: ProviderKindOAICompatLocal, Endpoint: "http://localhost:11434/v1"},
		{Name: "vllm", Kind: ProviderKindOAICompatLocal, Endpoint: "http://localhost:8000/v1"},
	}
}

// LookupKnownProvider returns the catalogue entry for name, case-insensitively.
func LookupKnownProvider(name string) (KnownProvider, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range KnownProviders() {
		if p.Name == want {
			return p, true
		}
	}
	return KnownProvider{}, false
}
