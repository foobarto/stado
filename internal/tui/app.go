package tui

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/anthropic"
	"github.com/foobarto/stado/internal/providers/google"
	"github.com/foobarto/stado/internal/providers/oaicompat"
	"github.com/foobarto/stado/internal/providers/openai"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/webfetch"
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

	sess, err := openSession(cfg, cwd)
	if err != nil {
		// Non-fatal: run without git state; tool-call audit will be skipped.
		fmt.Fprintf(os.Stderr, "stado: git state unavailable: %v (continuing without audit)\n", err)
	}
	exec := buildExecutor(sess, cfg)

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

// openSession opens (or creates) the sidecar + a fresh session for this TUI
// run. Worktree lives under XDG_STATE_HOME; the current cwd seeds the user
// repo root so alternates point at the user's .git/objects.
func openSession(cfg *config.Config, cwd string) (*stadogit.Session, error) {
	userRepo := findRepoRoot(cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		return nil, err
	}
	return stadogit.CreateSession(sc, cfg.WorktreeDir(), uuid.New().String(), plumbing.ZeroHash)
}

// buildExecutor wires the tool registry + sandbox runner + session so every
// tool call the model makes gets audited to the trace ref (and to tree for
// mutations).
func buildExecutor(sess *stadogit.Session, cfg *config.Config) *tools.Executor {
	reg := tools.NewRegistry()
	reg.Register(fs.ReadTool{})
	reg.Register(fs.WriteTool{})
	reg.Register(fs.EditTool{})
	reg.Register(fs.GlobTool{})
	reg.Register(fs.GrepTool{})
	reg.Register(bash.BashTool{Timeout: 60 * time.Second})
	reg.Register(webfetch.WebFetchTool{})
	return &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Agent:    "stado-tui",
		Model:    cfg.Defaults.Model,
	}
}

// findRepoRoot walks up looking for a .git dir. Falls back to start.
func findRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}
