package tui

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/anthropic"
	"github.com/foobarto/stado/internal/providers/google"
	"github.com/foobarto/stado/internal/providers/oaicompat"
	"github.com/foobarto/stado/internal/providers/openai"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// Run launches the TUI. Provider selection comes from cfg.Defaults.Provider:
//
//   - "anthropic" / "openai" / "google"  → direct SDK providers
//   - "ollama" / "llamacpp" / "vllm"      → OAI-compat presets
//   - "oaicompat:<url>"                   → OAI-compat with explicit endpoint
//   - anything else matching inference.presets.<name>.endpoint  → OAI-compat
func Run(cfg *config.Config) error {
	th, err := loadTheme(cfg)
	if err != nil {
		return fmt.Errorf("tui: theme: %w", err)
	}
	rnd, err := render.New(th)
	if err != nil {
		return fmt.Errorf("tui: render: %w", err)
	}

	cwd, _ := os.Getwd()
	keyReg := keys.NewRegistry()
	_ = keys.LoadOverrides(keyReg, cfg)

	sess, err := runtime.OpenSession(cfg, cwd)
	if err != nil {
		// Non-fatal: run without git state; tool-call audit will be skipped.
		fmt.Fprintf(os.Stderr, "stado: git state unavailable: %v (continuing without audit)\n", err)
	}
	exec := runtime.BuildExecutor(sess, cfg, "stado-tui")

	builder := func() (agent.Provider, error) { return buildProvider(cfg) }
	m := NewModel(cwd, cfg.Defaults.Model, cfg.Defaults.Provider, builder, rnd, keyReg)
	m.executor = exec
	m.session = sess
	m.SetContextThresholds(cfg.Context.SoftThreshold, cfg.Context.HardThreshold)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.Attach(p)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		p.Send(tea.Quit())
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// BuildProvider is the exported version of the internal provider-resolution
// switch, for use by `stado run` and other headless surfaces.
func BuildProvider(cfg *config.Config) (agent.Provider, error) { return buildProvider(cfg) }

func buildProvider(cfg *config.Config) (agent.Provider, error) {
	name := cfg.Defaults.Provider
	switch name {
	case "anthropic":
		return anthropic.New("")
	case "openai":
		return openai.New("", "")
	case "google", "gemini":
		return google.New("")
	case "":
		return nil, errors.New("no provider configured (set defaults.provider)")
	}

	// Bundled OAI-compat presets — known endpoints so users don't have to
	// write them out by hand. API key env var is picked up by oaicompat's
	// WithAPIKey option from the matching STADO_*_API_KEY.
	if ep, keyEnv, ok := builtinPreset(name); ok {
		opts := []oaicompat.Option{oaicompat.WithName(name)}
		if keyEnv != "" {
			if v := os.Getenv(keyEnv); v != "" {
				opts = append(opts, oaicompat.WithAPIKey(v))
			}
		}
		return oaicompat.New(ep, opts...)
	}

	// Look up user-defined inference presets from config.
	if cfg.Inference.Presets != nil {
		if preset, ok := cfg.Inference.Presets[name]; ok && preset.Endpoint != "" {
			return oaicompat.New(preset.Endpoint, oaicompat.WithName(name))
		}
	}
	return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, google, ollama, llamacpp, vllm, groq, openrouter, deepseek, xai, mistral, cerebras, litellm, lmstudio, or a configured preset)", name)
}

// builtinPreset returns (endpoint, api-key-env-var, ok) for bundled
// OAI-compat providers. API-key envs follow upstream convention so existing
// tooling keeps working.
func builtinPreset(name string) (string, string, bool) {
	switch name {
	// Local runners — no API key required.
	case "ollama":
		return "http://localhost:11434/v1", "", true
	case "llamacpp":
		return "http://localhost:8080/v1", "", true
	case "vllm":
		return "http://localhost:8000/v1", "", true
	case "lmstudio":
		return "http://localhost:1234/v1", "", true
	case "litellm":
		return "http://localhost:4000/v1", "LITELLM_API_KEY", true
	// Hosted services that speak OAI-compat.
	case "groq":
		return "https://api.groq.com/openai/v1", "GROQ_API_KEY", true
	case "openrouter":
		return "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", true
	case "deepseek":
		return "https://api.deepseek.com/v1", "DEEPSEEK_API_KEY", true
	case "xai":
		return "https://api.x.ai/v1", "XAI_API_KEY", true
	case "mistral":
		return "https://api.mistral.ai/v1", "MISTRAL_API_KEY", true
	case "cerebras":
		return "https://api.cerebras.ai/v1", "CEREBRAS_API_KEY", true
	}
	return "", "", false
}

func loadTheme(cfg *config.Config) (*theme.Theme, error) {
	// Look for ~/.config/stado/theme.toml alongside config.toml.
	themePath := filepath.Join(filepath.Dir(cfg.ConfigPath), "theme.toml")
	if _, err := os.Stat(themePath); err == nil {
		return theme.Load(themePath)
	}
	return theme.Default(), nil
}

