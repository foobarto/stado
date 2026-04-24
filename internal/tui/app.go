package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/anthropic"
	"github.com/foobarto/stado/internal/providers/google"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/providers/oaicompat"
	"github.com/foobarto/stado/internal/providers/openai"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/telemetry"
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
	done := tuiTraceCall("tui.Run")
	defer done()
	th, err := loadTheme(cfg)
	if err != nil {
		return fmt.Errorf("tui: theme: %w", err)
	}
	rnd, err := loadRenderer(cfg, th)
	if err != nil {
		return fmt.Errorf("tui: render: %w", err)
	}

	cwd, _ := os.Getwd()
	rootCtx := runtime.RootContext(cwd)
	runCtx, runSpan := otel.Tracer(telemetry.TracerName).Start(rootCtx, telemetry.SpanTUIRun,
		oteltrace.WithAttributes(attribute.String("session.worktree", cwd)))
	defer runSpan.End()
	keyReg := keys.NewRegistry()
	_ = keys.LoadOverrides(keyReg, cfg)

	sess, err := runtime.OpenSession(cfg, cwd)
	if err != nil {
		// Non-fatal: run without git state; tool-call audit will be skipped.
		fmt.Fprintf(os.Stderr, "stado: git state unavailable: %v (continuing without audit)\n", err)
	}
	exec, err := runtime.BuildExecutor(sess, cfg, "stado-tui")
	if err != nil {
		return fmt.Errorf("tui: tools: %w", err)
	}

	// The TUI may swap providers mid-session (e.g. `/model` picks a
	// detected local runner); pass a builder that reads the current
	// provider name from the Model so the rebuild honours the swap.
	var builder func() (agent.Provider, error)
	m := NewModel(cwd, cfg.Defaults.Model, cfg.Defaults.Provider, nil, rnd, keyReg)
	m.SetRootContext(runCtx)
	var localFallback *prewarmedLocalFallback
	if cfg.Defaults.Provider == "" {
		localFallback = startLocalFallbackPrewarm(runCtx, cfg)
		m.providerProbePending = true
		tuiTrace("startup provider prewarm started")
	}
	builder = func() (agent.Provider, error) {
		if m.providerName == "" && localFallback != nil {
			select {
			case <-localFallback.ready:
				if localFallback.provider != nil {
					logLocalFallback(localFallback.picked)
					return localFallback.provider, nil
				}
				return nil, noProviderConfiguredError()
			default:
			}
		}
		return buildProviderByName(cfg, m.providerName)
	}
	m.buildProvider = builder
	m.executor = exec
	m.session = sess
	m.systemPromptTemplate = cfg.Agent.SystemPromptTemplate
	if sess != nil {
		runSpan.SetAttributes(attribute.String("session.id", sess.ID))
	}
	m.SetContextThresholds(cfg.Context.SoftThreshold, cfg.Context.HardThreshold)
	m.SetBudget(cfg.Budget.WarnUSD, cfg.Budget.HardUSD)
	m.SetHooks(cfg.Hooks.PostTurn)
	if exec != nil {
		_, bashEnabled := exec.Registry.Get("bash")
		m.hookRunner.Disabled = !bashEnabled
	}
	m.SetApprovals(cfg.Approvals.Mode, cfg.Approvals.Allowlist)
	prevLogger := slog.Default()
	slog.SetDefault(newTUILogger(m))
	defer slog.SetDefault(prevLogger)
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
	defer m.closeBackgroundPlugins(context.Background())
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
	if localFallback != nil {
		go func() {
			<-localFallback.ready
			msg := localFallbackReadyMsg{provider: localFallback.provider}
			if localFallback.picked != nil {
				msg.providerName = localFallback.picked.Name
				msg.models = append([]string(nil), localFallback.picked.Models...)
			}
			m.sendMsg(msg)
		}()
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		p.Send(tea.Quit())
	}()

	if _, err := p.Run(); err != nil {
		runSpan.RecordError(err)
		runSpan.SetStatus(codes.Error, err.Error())
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
	// Read. The colour-spec "rgb:HHHH/HHHH/HHHH" is unmistakable, and
	// on slow splits the "rgb:" prefix may already be gone by the time
	// Bubble Tea emits the rune burst (e.g. "e/1e1e/1e1e\\").
	if isOSCColorReplyTail(runes) {
		return nil
	}
	return msg
}

func isOSCColorReplyTail(s string) bool {
	raw := strings.TrimSpace(s)
	hasOSCShape := strings.HasPrefix(raw, "]") ||
		strings.Contains(raw, "rgb:") ||
		strings.HasSuffix(raw, "\\") ||
		strings.HasSuffix(raw, "\a")
	if !hasOSCShape {
		return false
	}
	body := strings.TrimRight(raw, "\a\\")
	if strings.HasPrefix(body, "]") {
		if idx := strings.IndexByte(body, ';'); idx >= 0 {
			body = body[idx+1:]
		}
	}
	hadRGBPrefix := strings.HasPrefix(body, "rgb:")
	body = strings.TrimPrefix(body, "rgb:")
	parts := strings.Split(body, "/")
	if len(parts) != 3 && !(hadRGBPrefix && len(parts) >= 2) {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 4 {
			return false
		}
		for _, r := range p {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
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
		if p, picked := detectLocalFallback(context.Background(), cfg); p != nil {
			logLocalFallback(picked)
			return p, nil
		}
		return nil, noProviderConfiguredError()
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
			opts := []oaicompat.Option{oaicompat.WithName(name)}
			if key := config.ResolveProviderAPIKey(name); key != "" {
				opts = append(opts, oaicompat.WithAPIKey(key))
			}
			return oaicompat.New(preset.Endpoint, opts...)
		}
	}

	// Bundled OAI-compat presets — known endpoints so users don't have to
	// write them out by hand. API key env var is picked up by oaicompat's
	// WithAPIKey option from the matching STADO_*_API_KEY.
	if ep, _, ok := builtinPreset(name); ok {
		opts := []oaicompat.Option{oaicompat.WithName(name)}
		if key := config.ResolveProviderAPIKey(name); key != "" {
			opts = append(opts, oaicompat.WithAPIKey(key))
		}
		return oaicompat.New(ep, opts...)
	}
	return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, google, ollama, llamacpp, vllm, groq, openrouter, deepseek, xai, mistral, cerebras, litellm, lmstudio, or a configured preset)", name)
}

func noProviderConfiguredError() error {
	return errors.New("no provider configured and no local inference runner detected — " +
		"set defaults.provider in config (e.g. 'anthropic', 'openai', 'ollama', 'lmstudio') " +
		"or start a local server (ollama serve / llama-server / lmstudio / vllm)")
}

// detectLocalFallback probes bundled local runners (+ user-configured
// localhost presets) concurrently with a short budget. Returns the
// first reachable OAI-compat provider — used when the default
// "anthropic" provider is selected without an API key. Returns nil
// when no local runner answers; the caller then falls through to the
// original anthropic path so the user sees the canonical
// "ANTHROPIC_API_KEY not set" error with no mysterious behaviour.
func detectLocalFallback(ctx context.Context, cfg *config.Config) (agent.Provider, *localdetect.Result) {
	done := tuiTraceCall("tui.detectLocalFallback")
	defer done()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, span := otel.Tracer(telemetry.TracerName).Start(ctx, telemetry.SpanTUIProviderProbe,
		oteltrace.WithAttributes(attribute.Int("probe.user_presets", len(cfg.Inference.Presets))))
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	presets := map[string]string{}
	for name, p := range cfg.Inference.Presets {
		presets[name] = p.Endpoint
	}
	results := localdetect.Detect(ctx, localdetect.MergeUserPresets(presets))
	tuiTrace("local fallback probe finished", "candidates", len(results))
	span.SetAttributes(attribute.Int("probe.candidates", len(results)))
	if picked := pickLocalFallback(results); picked != nil {
		p, err := oaicompat.New(picked.Endpoint, oaicompat.WithName(picked.Name))
		if err == nil {
			tuiTrace("local fallback picked",
				"provider", picked.Name,
				"endpoint", picked.Endpoint,
				"models", len(picked.Models))
			span.SetAttributes(
				attribute.String("provider.name", picked.Name),
				attribute.String("provider.endpoint", picked.Endpoint),
				attribute.Int("provider.models", len(picked.Models)),
			)
			return p, picked
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	tuiTrace("local fallback unavailable")
	span.SetAttributes(attribute.Bool("probe.reachable", false))
	return nil, nil
}

type prewarmedLocalFallback struct {
	ready    chan struct{}
	provider agent.Provider
	picked   *localdetect.Result
}

func startLocalFallbackPrewarm(ctx context.Context, cfg *config.Config) *prewarmedLocalFallback {
	out := &prewarmedLocalFallback{ready: make(chan struct{})}
	go func() {
		out.provider, out.picked = detectLocalFallback(ctx, cfg)
		close(out.ready)
	}()
	return out
}

func logLocalFallback(picked *localdetect.Result) {
	if picked == nil {
		return
	}
	slog.Info("no provider configured; using detected local fallback",
		"provider", picked.Name,
		"endpoint", picked.Endpoint)
}

func pickLocalFallback(results []localdetect.Result) *localdetect.Result {
	var reachable *localdetect.Result
	for i := range results {
		r := &results[i]
		if !r.Reachable {
			continue
		}
		if len(r.Models) > 0 {
			return r
		}
		if reachable == nil {
			reachable = r
		}
	}
	return reachable
}

// builtinPreset returns (endpoint, api-key-env-var, ok) for bundled
// OAI-compat providers. API-key envs follow upstream convention so existing
// tooling keeps working.
func builtinPreset(name string) (string, string, bool) {
	return config.BuiltinInferencePreset(name)
}

func loadTheme(cfg *config.Config) (*theme.Theme, error) {
	// Look for ~/.config/stado/theme.toml alongside config.toml.
	themePath := filepath.Join(filepath.Dir(cfg.ConfigPath), "theme.toml")
	if _, err := os.Stat(themePath); err == nil {
		return theme.Load(themePath)
	}
	return theme.Default(), nil
}

func loadRenderer(cfg *config.Config, th *theme.Theme) (*render.Renderer, error) {
	overlayDir := filepath.Join(filepath.Dir(cfg.ConfigPath), "templates")
	info, err := os.Stat(overlayDir)
	if err == nil && info.IsDir() {
		return render.NewWithOverlay(th, overlayDir)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("render overlay: %w", err)
	}
	return render.New(th)
}
