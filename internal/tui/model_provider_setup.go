package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/foobarto/stado/internal/config"
)

func (m *Model) showSelectedProviderSetup() {
	provider := m.providerName
	if sel := m.modelPicker.Selected(); sel != nil && strings.TrimSpace(sel.ProviderName) != "" {
		provider = sel.ProviderName
	}
	m.modelPicker.Close()
	m.appendBlock(block{kind: "system", body: m.providerSetupBody(provider)})
	m.layout()
}

func (m *Model) providerSetupBody(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "provider setup: no provider selected\n- set `defaults.provider` in config.toml, start a local runner, or pick a detected model from `/model`."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "provider setup: %s\n", provider)
	if preset, endpoint, ok := m.configuredPresetEntry(provider); ok {
		fmt.Fprintf(&b, "- configured preset endpoint: %s\n", endpoint)
		env := config.PresetAPIKeyEnv(provider, preset)
		writePresetAPIKeySetup(&b, provider, endpoint, env, preset.APIKeyEnv != "")
		return strings.TrimRight(b.String(), "\n")
	}
	if endpoint, _, ok := config.BuiltinInferencePreset(provider); ok {
		fmt.Fprintf(&b, "- bundled endpoint: %s\n", endpoint)
		writeAPIKeySetup(&b, provider, endpoint)
		writeLocalRunnerSetup(&b, provider)
		return strings.TrimRight(b.String(), "\n")
	}
	if env := config.ProviderAPIKeyEnv(provider); env != "" {
		writeAPIKeySetup(&b, provider, "")
		return strings.TrimRight(b.String(), "\n")
	}

	fmt.Fprintf(&b, "- no bundled setup recipe is available.\n")
	fmt.Fprintf(&b, "- add `[inference.presets.%s].endpoint` to config.toml, then select a model or run `/model <id>`.", provider)
	return b.String()
}

func providerCredentialHealth(provider string) (value, tone, action string) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "not configured", "muted", "/provider <name>"
	}
	env := config.ProviderAPIKeyEnv(provider)
	if env == "" {
		return "not required by preset", "muted", "/providers"
	}
	if os.Getenv(env) == "" {
		return "missing " + env, "warning", "/model Ctrl+A"
	}
	return env + " set", "text", "/model Ctrl+A"
}

func (m *Model) configuredPresetEntry(provider string) (config.InferencePreset, string, bool) {
	if m.cfg == nil || m.cfg.Inference.Presets == nil {
		return config.InferencePreset{}, "", false
	}
	preset, ok := m.cfg.Inference.Presets[provider]
	if !ok || strings.TrimSpace(preset.Endpoint) == "" {
		return config.InferencePreset{}, "", false
	}
	return preset, preset.Endpoint, true
}

func writeAPIKeySetup(b *strings.Builder, provider, endpoint string) {
	writePresetAPIKeySetup(b, provider, endpoint, config.ProviderAPIKeyEnv(provider), false)
}

// writePresetAPIKeySetup is the underlying writer used by both the
// provider-level help (no preset object) and the preset-level help
// (preset.APIKeyEnv may pin a non-conventional env var). When env is
// empty and the endpoint is non-local, the message tells the user how
// to opt into authenticated access.
func writePresetAPIKeySetup(b *strings.Builder, provider, endpoint, env string, explicit bool) {
	if env == "" {
		if isLocalEndpoint(endpoint) {
			fmt.Fprintf(b, "- no API key is expected by default.\n")
		} else {
			fmt.Fprintf(b, "- if this endpoint requires auth, set `[inference.presets.%s].api_key_env = \"<NAME>\"` and export that env var.\n", provider)
		}
		return
	}
	if isLocalEndpoint(endpoint) {
		if os.Getenv(env) == "" {
			fmt.Fprintf(b, "- set `%s` only if your local proxy requires auth.\n", env)
		} else {
			fmt.Fprintf(b, "- `%s` is set in this process.\n", env)
		}
		return
	}
	if os.Getenv(env) == "" {
		fmt.Fprintf(b, "- missing credentials: export `%s=...` before starting stado.\n", env)
	} else {
		fmt.Fprintf(b, "- credentials: `%s` is set in this process.\n", env)
	}
	if explicit {
		fmt.Fprintf(b, "- env var pinned via `[inference.presets.%s].api_key_env`.\n", provider)
	}
	fmt.Fprintf(b, "- keep secrets in your shell, OS keychain, or service manager, not config.toml.\n")
}

func writeLocalRunnerSetup(b *strings.Builder, provider string) {
	switch provider {
	case "ollama":
		fmt.Fprintf(b, "- start Ollama with `ollama serve`, pull a model, then reopen `/model`.\n")
	case "lmstudio":
		fmt.Fprintf(b, "- start the LM Studio local server, load a model in the developer page or with `lms load <model>`, then reopen `/model`.\n")
	case "llamacpp":
		fmt.Fprintf(b, "- start llama.cpp with `llama-server -m <model>`, then reopen `/model`.\n")
	case "vllm":
		fmt.Fprintf(b, "- start vLLM with `vllm serve <model>`, then reopen `/model`.\n")
	}
}

func isLocalEndpoint(endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	return strings.HasPrefix(endpoint, "http://localhost:") ||
		strings.HasPrefix(endpoint, "http://127.0.0.1:") ||
		strings.HasPrefix(endpoint, "http://[::1]:")
}
