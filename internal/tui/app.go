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
	case "ollama":
		return oaicompat.New("http://localhost:11434/v1", oaicompat.WithName("ollama"))
	case "llamacpp":
		return oaicompat.New("http://localhost:8080/v1", oaicompat.WithName("llamacpp"))
	case "vllm":
		return oaicompat.New("http://localhost:8000/v1", oaicompat.WithName("vllm"))
	case "":
		return nil, errors.New("no provider configured (set defaults.provider)")
	}

	// Look up inference presets.
	if cfg.Inference.Presets != nil {
		if preset, ok := cfg.Inference.Presets[name]; ok && preset.Endpoint != "" {
			return oaicompat.New(preset.Endpoint, oaicompat.WithName(name))
		}
	}
	return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, google, ollama, llamacpp, vllm, or a configured preset)", name)
}

func loadTheme(cfg *config.Config) (*theme.Theme, error) {
	// Look for ~/.config/stado/theme.toml alongside config.toml.
	themePath := filepath.Join(filepath.Dir(cfg.ConfigPath), "theme.toml")
	if _, err := os.Stat(themePath); err == nil {
		return theme.Load(themePath)
	}
	return theme.Default(), nil
}

