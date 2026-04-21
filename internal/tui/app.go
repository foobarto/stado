package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/anthropic"
	"github.com/foobarto/stado/internal/providers/google"
	"github.com/foobarto/stado/internal/providers/localdetect"
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

	// The TUI may swap providers mid-session (e.g. `/model` picks a
	// detected local runner); pass a builder that reads the current
	// provider name from the Model so the rebuild honours the swap.
	var builder func() (agent.Provider, error)
	m := NewModel(cwd, cfg.Defaults.Model, cfg.Defaults.Provider, nil, rnd, keyReg)
	builder = func() (agent.Provider, error) {
		return buildProviderByName(cfg, m.providerName)
	}
	m.buildProvider = builder
	m.executor = exec
	m.session = sess
	m.SetContextThresholds(cfg.Context.SoftThreshold, cfg.Context.HardThreshold)
	m.SetBudget(cfg.Budget.WarnUSD, cfg.Budget.HardUSD)
	m.SetHooks(cfg.Hooks.PostTurn)
	m.SetApprovals(cfg.Approvals.Mode, cfg.Approvals.Allowlist)
	// If we booted into a worktree that a prior `stado session fork`
	// wrote a `.stado-span-context` into, wrap the TUI's ancestor
	// context so every subsequent span links back to the fork event's
	// trace tree — Phase 9.4/9.5 cross-process span link.
	m.SetRootContext(runtime.RootContext(cwd))
	// Replay any persisted conversation from the session's worktree so
	// "kill stado and come back" picks up where the user left off.
	// No-op on fresh sessions — conversation.jsonl only exists after
	// at least one message has been written.
	m.LoadPersistedConversation()
	// Load declared background plugins (cfg.Plugins.Background). Each
	// ticks once per turn boundary and can observe/fork the session
	// via the host-import ABI. Failures are advisory — a bad plugin
	// shouldn't brick the TUI.
	m.LoadBackgroundPlugins(cfg)
	// Wrap stdin with an OSC-response stripper. See osc_reader.go:
	// the terminal's late replies to lipgloss/termenv's one-shot
	// background-colour query would otherwise leak into the focused
	// widget as literal text. tea.WithFilter below is the backstop for
	// responses that slipped past the wrapper (shouldn't happen but
	// costs nothing to keep).
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithInput(newOSCStripFile(os.Stdin)),
		tea.WithFilter(filterOSCResponses))
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

// filterOSCResponses is the backstop filter for terminal OSC replies
// that slipped past the byte-level newOSCStripReader (see
// osc_reader.go). The reader handles the common case; this filter
// catches the Alt-prefixed-runes shape bubbletea v1.3's input parser
// synthesises when an ESC is followed by the OSC payload in the same
// Read — we intercept the synthesised KeyMsg before it reaches the
// textarea. Removed once we upgrade to bubbletea v2 (native OSC
// parser). Also drops payload-only bursts that start with "rgb:" —
// a split OSC 10/11/12 tail where the leading ']NN;' has already
// been consumed but the colour spec leaked.
func filterOSCResponses(_ tea.Model, msg tea.Msg) tea.Msg {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return msg
	}
	if len(km.Runes) == 0 {
		return msg
	}
	runes := string(km.Runes)
	// Alt-prefixed '<digit>...;' form: shape of an OSC status-report
	// reply where bubbletea still saw the opening ESC.
	if km.Alt && km.Runes[0] == ']' && len(km.Runes) >= 3 {
		r := km.Runes[1]
		if r >= '0' && r <= '9' {
			return nil
		}
	}
	// Payload-only tail when the ]NN; prefix was consumed in a prior
	// Read. The colour-spec "rgb:HHHH/HHHH/HHHH" is unmistakable;
	// drop any rune burst that contains it. Rules out legit user
	// input because "rgb:" followed by four-hex/slash-four-hex is
	// not a sequence humans type.
	if strings.Contains(runes, "rgb:") && strings.Contains(runes, "/") {
		return nil
	}
	return msg
}

// BuildProvider is the exported version of the internal provider-resolution
// switch, for use by `stado run` and other headless surfaces.
func BuildProvider(cfg *config.Config) (agent.Provider, error) { return buildProvider(cfg) }

func buildProvider(cfg *config.Config) (agent.Provider, error) {
	return buildProviderByName(cfg, cfg.Defaults.Provider)
}

// buildProviderByName resolves a provider by explicit name override. The
// TUI uses this to rebuild after the user swaps providers mid-session
// (e.g. via the /model picker choosing a detected local runner).
//
// An empty `name` means "no provider configured" — the function probes
// bundled + user-preset localhost endpoints (ollama / lmstudio /
// llamacpp / vllm / custom presets) concurrently and picks the first
// reachable one. No hosted provider (anthropic / openai / google) is
// assumed as a default; if nothing answers and nothing is configured,
// the caller gets a clear error telling them to set defaults.provider.
// Once any name is set explicitly, it's taken at face value — no probe.
func buildProviderByName(cfg *config.Config, name string) (agent.Provider, error) {
	if name == "" {
		if p := detectLocalFallback(cfg); p != nil {
			return p, nil
		}
		return nil, errors.New("no provider configured and no local inference runner detected — " +
			"set defaults.provider in config (e.g. 'anthropic', 'openai', 'ollama', 'lmstudio') " +
			"or start a local server (ollama serve / llama-server / lmstudio / vllm)")
	}

	switch name {
	case "anthropic":
		return anthropic.New("")
	case "openai":
		return openai.New("", "")
	case "google", "gemini":
		return google.New("")
	}

	// User-defined preset wins over the bundled default of the same name
	// — lets operators point `lmstudio` at a non-default port, etc.,
	// without writing a whole new preset or setting an env var.
	// STADO_INFERENCE_PRESETS_<NAME>_ENDPOINT=... also lands here via koanf.
	if cfg.Inference.Presets != nil {
		if preset, ok := cfg.Inference.Presets[name]; ok && preset.Endpoint != "" {
			return oaicompat.New(preset.Endpoint, oaicompat.WithName(name))
		}
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
	return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, google, ollama, llamacpp, vllm, groq, openrouter, deepseek, xai, mistral, cerebras, litellm, lmstudio, or a configured preset)", name)
}

// detectLocalFallback probes bundled local runners (+ user-configured
// localhost presets) concurrently with a short budget. Returns the
// first reachable OAI-compat provider — used when the default
// "anthropic" provider is selected without an API key. Returns nil
// when no local runner answers; the caller then falls through to the
// original anthropic path so the user sees the canonical
// "ANTHROPIC_API_KEY not set" error with no mysterious behaviour.
func detectLocalFallback(cfg *config.Config) agent.Provider {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	presets := map[string]string{}
	for name, p := range cfg.Inference.Presets {
		presets[name] = p.Endpoint
	}
	results := localdetect.Detect(ctx, localdetect.MergeUserPresets(presets))
	for _, r := range results {
		if !r.Reachable {
			continue
		}
		p, err := oaicompat.New(r.Endpoint, oaicompat.WithName(r.Name))
		if err != nil {
			continue
		}
		fmt.Fprintf(os.Stderr,
			"stado: no provider configured — using local %s at %s (set defaults.provider in config to pin)\n",
			r.Name, r.Endpoint)
		return p
	}
	return nil
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

