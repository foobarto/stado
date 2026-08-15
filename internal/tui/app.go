package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/fs"
	"github.com/foobarto/stado/internal/harness"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/integrations"
	"github.com/foobarto/stado/internal/lspfind"
	"github.com/foobarto/stado/internal/providers/acpwrap"
	"github.com/foobarto/stado/internal/providers/anthropic"
	"github.com/foobarto/stado/internal/providers/google"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/providers/mcpwrap"
	"github.com/foobarto/stado/internal/providers/oaicompat"
	"github.com/foobarto/stado/internal/providers/openai"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/runtime/sessionstats"
	"github.com/foobarto/stado/internal/sandbox"
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
func Run(cfg *config.Config, startupNotices []string, executorSandbox runtime.ExecutorSandbox, metrics telemetry.Metrics, brokerController runtime.BrokerController) error {
	done := tuiTraceCall("tui.Run")
	defer done()
	// startupNotices carries the pre-launch banner (sandbox posture,
	// broker session, writable paths) captured by the caller, which the
	// alt-screen would otherwise swallow. Run appends its own startup
	// warnings to it as they occur, then renders the lot as one system
	// block (see m.injectStartupNotices below).
	notices := append([]string(nil), startupNotices...)
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
	keyReg := keys.NewRegistryForSchema(cfg.Keymap.Schema)
	if err := keys.LoadOverrides(keyReg, cfg); err != nil {
		// Non-fatal: a bad keymap override (e.g. an unknown action name) is a
		// stderr warning, not a boot failure — the valid overrides still
		// applied. Matches the theme / skills / instructions load idiom.
		fmt.Fprintf(os.Stderr, "stado: keymap overrides: %v\n", err)
	}

	sess, err := runtime.OpenSession(cfg, cwd)
	if err != nil {
		// Non-fatal: run without git state; tool-call audit will be skipped.
		notices = append(notices, fmt.Sprintf("stado: git state unavailable: %v (continuing without audit)", err))
	}
	exec, err := runtime.BuildExecutor(sess, cfg, "stado-tui", metrics)
	if err != nil {
		return fmt.Errorf("tui: tools: %w", err)
	}
	executorSandbox.Apply(exec)

	// The TUI may swap providers mid-session (e.g. `/model` picks a
	// detected local runner); pass a builder that reads the current
	// provider name from the Model so the rebuild honours the swap.
	var builder func() (agent.Provider, error)
	m := NewModel(cwd, cfg.Defaults.Model, cfg.Defaults.Provider, nil, rnd, keyReg)
	m.cfg = cfg
	m.metrics = metrics
	m.brokerRoot = brokerController
	m.broker = brokerController
	m.verifyConfig = runtime.VerifyConfigFrom(cfg)
	m.verifyEnabled = m.verifyConfig.Enabled()
	// EP-0030: security harness — prepend to the system prompt in security mode,
	// same as `stado run`. Previously the [harness].mode config knob was honored
	// only by `stado run`, silently ignored by the TUI.
	m.systemPrompt = harness.Prepend(m.systemPrompt, cwd, cfg.Harness.Mode)
	// Modal vim (keymap Phase 2): enable the modal engine only for the "vim"
	// schema. Inert for emacs/vscode/custom. Starts in INSERT (set inside).
	m.SetKeymapSchema(cfg.Keymap.Schema)
	if cfg.TUI.SidebarWidth > 0 {
		// Clamp the persisted width to the valid range — a hand-edited
		// config could otherwise drive an out-of-range layout.
		m.sidebarWidth = m.clampSidebarWidth(cfg.TUI.SidebarWidth)
	}
	// #21: configured chrome layout (empty → defaults).
	m.sidebarSections = normalizeSidebarSections(cfg.TUI.Sidebar.Sections)
	m.footerSegments = normalizeFooterSegments(cfg.TUI.Footer.Segments)
	m.applyConfiguredThinkingDisplay(cfg)
	m.applyConfiguredToolDisplay(cfg)
	if m.systemPromptPath != "" && !instructions.TemplateInjectsProjectInstructions(cfg.Agent.SystemPromptTemplate) {
		notices = append(notices, fmt.Sprintf(
			"stado: warning — system prompt template at %s does not include {{ .ProjectInstructions }}; project rules from %s will not reach the model. Add the block or delete the file to regenerate the default.",
			cfg.Agent.SystemPromptPath, m.systemPromptPath))
	}
	m.SetRootContext(runCtx)
	if sess != nil {
		activeBroker, owned, peerErr := m.openSessionBroker(runCtx, sess)
		if peerErr != nil {
			return fmt.Errorf("tui: %w", peerErr)
		}
		m.broker = activeBroker
		m.brokerPeerOwned = owned
	}
	if exec != nil {
		exec.DispatchGate = runtime.SchedulingDispatchGate(m.broker)
	}
	// Cancel any background fleet agents on exit. They spawn off a
	// non-cancellable root context, so without this they outlive the UI as
	// orphaned goroutines / child processes (EP-0034). Deferred so it fires on
	// the clean path and the error-return path alike.
	defer m.Shutdown()
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
	m.executorSandbox = executorSandbox
	m.executor = exec
	m.session = sess
	m.systemPromptTemplate = cfg.Agent.SystemPromptTemplate
	m.initPersona(cfg)
	if sess != nil {
		runSpan.SetAttributes(attribute.String("session.id", sess.ID))
	}
	m.SetContextThresholds(cfg.Context.SoftThreshold, cfg.Context.HardThreshold)
	m.SetBudgetTokens(cfg.Budget.WarnTokens, cfg.Budget.HardTokens)
	m.SetBudgetTokensSplit(cfg.Budget.WarnInputTokens, cfg.Budget.HardInputTokens, cfg.Budget.WarnOutputTokens, cfg.Budget.HardOutputTokens)
	m.SetHooks(cfg.Hooks.PostTurn)
	// F1: scriptable deny/mutate lifecycle hooks (Lua). Same runner on the
	// executor (tool-side pre/post-tool seam) and the model (post_turn).
	//
	// C3: collect skip-warnings (broken/unloadable hook) rather than letting
	// them go to stderr, which the alt-screen swallows. Fold them into the
	// startup notices so they surface in-band as a system block.
	lifecycleHooks, hookWarnings := hooks.BuildLifecycleRunnerWithWarnings(cfg)
	notices = append(notices, hookWarnings...)
	m.lifecycleHooks = lifecycleHooks
	if exec != nil {
		exec.Hooks = lifecycleHooks
	}
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
	// Render the captured startup banner as a system block so it lands
	// in the scrollback the alt-screen would otherwise have cleared.
	// Injected after the replay so a resumed session shows prior history
	// first, then this launch's notice.
	m.injectStartupNotices(notices)
	// Load declared background plugins (cfg.Plugins.Background). Each
	// ticks once per turn boundary and can observe/fork the session
	// via the host-import ABI. Failures are advisory — a bad plugin
	// shouldn't brick the TUI.
	m.LoadBackgroundPlugins(cfg)
	m.registerSkillSlashCommands(func(string) {})
	// Native fact-only diagnostics observe after operator Lua and signed WASM
	// application policy. Keeping this append here makes the EP-0064 order
	// executable: Lua (config order), WASM (canonical identity), native
	// observers last.
	if m.cfg != nil && m.cfg.LSP.AutoDiagnostics && m.lspManager != nil && m.lspDiagnostics != nil {
		diagHook := lspfind.NewDiagnosticsHook(m.lspManager, m.lspDiagnostics, m.cwd)
		m.lifecycleHooks = m.lifecycleHooks.Append(diagHook)
		if m.executor != nil {
			m.executor.Hooks = m.lifecycleHooks
		}
	}
	defer m.closeBackgroundPlugins(context.Background())
	// Wrap stdin with an OSC-response stripper. See osc_reader.go:
	// the terminal's late replies to lipgloss/termenv's one-shot
	// background-colour query would otherwise leak into the focused
	// widget as literal text. tea.WithFilter below is the backstop for
	// responses that slipped past the wrapper (shouldn't happen but
	// costs nothing to keep).
	// AltScreen + mouse mode are no longer program options in v2 — they
	// are set per-frame on the tea.View returned by Model.View (see
	// model_render.go). Mouse capture state is read there from
	// m.cfg.TUI.MouseCapture.
	teaOpts := []tea.ProgramOption{
		tea.WithInput(newOSCStripFile(os.Stdin)),
		tea.WithFilter(filterOSCResponses),
	}
	p := tea.NewProgram(m, teaOpts...)
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
	// At-quit session summary (Q1). Walks the session's git-native
	// trace ref and prints uptime / tokens / cost / per-model + per-
	// tool breakdown to stderr after the TUI has restored the
	// terminal. Best-effort: missing trace ref or empty session
	// renders a single "no tool calls" line. Failure to walk the
	// ref shouldn't override the clean exit.
	emitSessionSummary(m, os.Stderr)
	return nil
}

// Shutdown releases resources that outlive the Bubble Tea event loop. Deferred
// by Run so it fires on every exit path. Currently: cancel any background
// fleet agents (EP-0034) — they run off a non-cancellable root context, so
// without this they (and the provider calls / child processes they drive)
// leak past the UI as orphans.
func (m *Model) Shutdown() {
	if m.fleet != nil {
		m.fleet.CancelAll()
	}
	// Run normally closes application modules first via closeBackgroundPlugins.
	// Keep Shutdown independently safe for tests and early-return paths, then
	// retire only the non-owning active peer. The root daemon connection belongs
	// to the command entry point and deliberately survives until Run returns.
	m.closeLifecycleApplications(context.Background())
	_ = m.closeActiveBrokerPeer()
}

// emitSessionSummary walks the session's trace ref and writes a
// human-readable summary to w. Pulled out for testability — callers
// pass a real *Model and an io.Writer.
func emitSessionSummary(m *Model, w io.Writer) {
	if m == nil || m.session == nil || m.session.Sidecar == nil {
		return
	}
	summary, err := sessionstats.Walk(m.session.Sidecar, m.session.ID)
	if err != nil {
		fmt.Fprintf(w, "stado: warn: session summary walk failed: %v\n", err)
		return
	}
	uptime := time.Since(m.startedAt)
	if m.startedAt.IsZero() {
		uptime = 0
	}
	sessionstats.Render(w, summary, uptime)
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
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return msg
	}
	text := km.Text
	if text == "" {
		return msg
	}
	runes := []rune(text)
	// Alt-prefixed '<digit>...;' form: shape of an OSC status-report
	// reply where bubbletea still saw the opening ESC.
	if km.Mod.Contains(tea.ModAlt) && runes[0] == ']' && len(runes) >= 3 {
		if r := runes[1]; r >= '0' && r <= '9' {
			return nil
		}
	}
	// Payload-only tail when the ]NN; prefix was consumed in a prior
	// Read. The colour-spec "rgb:HHHH/HHHH/HHHH" is unmistakable, and
	// on slow splits the "rgb:" prefix may already be gone by the time
	// Bubble Tea emits the rune burst (e.g. "e/1e1e/1e1e\\").
	if isOSCColorReplyTail(text) {
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

// buildIntegrationFallback synthesises an ACP/MCP-wrapped provider
// from the integrations registry when no matching config entry
// exists. Returns (provider, nil, true) on success; (nil, err, true)
// when we matched an integration but couldn't build the provider
// (e.g. binary not detected on this host); (nil, nil, false) when
// the name doesn't look like an integration auto-fallback target.
//
// The trailing-bool form lets the caller distinguish "tried and
// failed" from "didn't try" — failed lookups cascade to the
// inference-preset path so a misnamed provider gets the standard
// "unknown provider" error rather than a confusing integrations-
// specific message.
func buildIntegrationFallback(cfg *config.Config, name string) (agent.Provider, error, bool) {
	var (
		base  string
		isMCP bool
	)
	switch {
	case strings.HasSuffix(name, "-acp"):
		base = strings.TrimSuffix(name, "-acp")
	case strings.HasSuffix(name, "-mcp"):
		base = strings.TrimSuffix(name, "-mcp")
		isMCP = true
	default:
		return nil, nil, false
	}

	dets := integrations.DetectInstalled(context.Background())
	for _, d := range dets {
		if d.Name != base {
			continue
		}
		if d.BinaryPath == "" {
			return nil, fmt.Errorf("provider %q: %s detected via config but no runnable binary found", name, base), true
		}
		if isMCP {
			tools := d.MCPWrapTools
			if tools[0] == "" {
				return nil, fmt.Errorf("provider %q: %s does not have an MCP-wrap profile (no MCPWrapTools defined)", name, base), true
			}
			p, err := mcpwrap.New(mcpwrap.Config{
				Name:         name,
				Binary:       d.BinaryPath,
				Args:         d.MCPWrapServerArgs,
				CallTool:     tools[0],
				ContinueTool: tools[1],
			})
			return p, err, true
		}
		// ACP path.
		if len(d.ACPArgs) == 0 {
			return nil, fmt.Errorf("provider %q: %s does not expose a stdio ACP-agent mode (try %s-mcp if available)", name, base, base), true
		}
		p, err := acpwrap.New(acpwrap.Config{
			Name:   name,
			Binary: d.BinaryPath,
			Args:   d.ACPArgs,
		})
		return p, err, true
	}
	return nil, fmt.Errorf("provider %q: agent %q not detected on this host (run `stado integrations` to see what's installed)", name, base), true
}

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

	// ACP-wrapped agent providers (`[acp.providers.<name>]` in
	// config.toml). Picked BEFORE the inference-preset lookup so an
	// ACP wrapper named "gemini-acp" doesn't get shadowed by an
	// OAI-compat preset of the same name. EP-0032 phase A.
	if cfg.ACP.Providers != nil {
		if p, ok := cfg.ACP.Providers[name]; ok && p.Binary != "" {
			ac := acpwrap.Config{
				Name:        name,
				Binary:      p.Binary,
				Args:        p.Args,
				CWD:         p.CWD,
				Env:         p.Env,
				InheritEnv:  p.InheritEnv,
				Tools:       p.Tools,
				RegisterMCP: p.RegisterMCP, // EP-0032: forward auto-registration consent
			}
			// EP-0032 phase B: when tools = "stado" the provider
			// needs a ToolHostConfig to dispatch inbound fs/* ACP
			// requests through stado's read/write tools. Build a
			// default Host (auto-approve + sandbox.Detect()
			// runner) and wire the bundled fs tools. Custom dispatch
			// (different tool implementations, alternate Host) can
			// be supplied by future call sites that build the Config
			// directly — here we use the bundled defaults.
			if p.Tools == "stado" {
				cwd := p.CWD
				if cwd == "" {
					if c, err := os.Getwd(); err == nil {
						cwd = c
					}
				}
				ac.ToolHostCfg = acpwrap.ToolHostConfig{
					ReadTool:  fs.ReadTool{},
					WriteTool: fs.WriteTool{},
					Host:      acpwrap.NewDefaultHost(cwd, sandbox.Detect()),
				}
			}
			return acpwrap.New(ac)
		}
	}

	// MCP-wrapped agent providers (`[mcp.providers.<name>]` in
	// config.toml). For coding-agent CLIs that don't expose stdio
	// ACP-agent mode but DO expose themselves via MCP — codex being
	// the canonical example. Picked before inference-preset lookup
	// so a wrapper-named provider doesn't get shadowed by a same-
	// named OAI-compat preset.
	if cfg.MCP.Providers != nil {
		if p, ok := cfg.MCP.Providers[name]; ok && p.Binary != "" {
			return mcpwrap.New(mcpwrap.Config{
				Name:              name,
				Binary:            p.Binary,
				Args:              p.Args,
				CallTool:          p.CallTool,
				ContinueTool:      p.ContinueTool,
				PromptArgKey:      p.PromptArgKey,
				ThreadIDArgKey:    p.ThreadIDArgKey,
				ContentResultKey:  p.ContentResultKey,
				ThreadIDResultKey: p.ThreadIDResultKey,
				CallToolOverrides: p.CallToolOverrides,
				Env:               p.Env,
				InheritEnv:        p.InheritEnv,
			})
		}
	}

	// Integrations auto-fallback: when the user requests a provider
	// like "<agent>-acp" or "<agent>-mcp" without a matching config
	// entry, look it up in the integrations registry. If the agent
	// is detected on PATH (or at a known well-known path like
	// hermes's venv binary), synthesize an acpwrap/mcpwrap provider
	// on the fly. Saves users from having to write
	// [acp.providers.gemini-acp] etc. for every agent stado already
	// knows about.
	if p, err, ok := buildIntegrationFallback(cfg, name); ok {
		return p, err
	}

	// User-defined preset wins over the bundled default of the same name
	// — lets operators point `lmstudio` at a non-default port, etc.,
	// without writing a whole new preset or setting an env var.
	// STADO_INFERENCE_PRESETS_<NAME>_ENDPOINT=... also lands here via koanf.
	if cfg.Inference.Presets != nil {
		if preset, ok := cfg.Inference.Presets[name]; ok && preset.Endpoint != "" {
			opts := []oaicompat.Option{oaicompat.WithName(name)}
			if key := config.ResolvePresetAPIKey(name, preset); key != "" {
				opts = append(opts, oaicompat.WithAPIKey(key))
			}
			return oaicompat.New(preset.Endpoint, opts...)
		}
	}

	// Bundled Anthropic-compatible cloud endpoints (e.g.
	// minimax-anthropic → MiniMax's Claude-compatible Coding Plan
	// endpoint). Routed through the native anthropic SDK with a
	// base-URL override so prompt caching / thinking work, unlike the
	// OAI-compat path. Picked before the OAI-compat builtin lookup.
	//
	// An [inference.presets.<name>] block written by `stado auth set`
	// overrides the bundled defaults: base_url points the SDK at a custom
	// host, and api_key_env names a non-conventional env var. Both fall
	// back to the registry defaults (kp.Endpoint / kp.APIKeyEnv) when
	// unset, so a bare `minimax-anthropic` still works untouched.
	if kp, ok := config.LookupKnownProvider(name); ok && kp.Kind == config.ProviderKindAnthropicCompatCloud {
		baseURL := kp.Endpoint
		keyEnv := kp.APIKeyEnv
		if cfg.Inference.Presets != nil {
			if preset, ok := cfg.Inference.Presets[name]; ok {
				if bu := strings.TrimSpace(preset.BaseURL); bu != "" {
					baseURL = bu
				}
				if ke := strings.TrimSpace(preset.APIKeyEnv); ke != "" {
					keyEnv = ke
				}
			}
		}
		key := config.ResolveProviderSecret(keyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s: %s not set", name, keyEnv)
		}
		return anthropic.New(key, anthropic.WithBaseURL(baseURL), anthropic.WithName(name))
	}

	// Bundled OAI-compat presets — known endpoints so users don't have to
	// write them out by hand. API key env var follows the builtin
	// convention (LITELLM_API_KEY, OLLAMA_CLOUD_API_KEY, etc.).
	if ep, _, ok := builtinPreset(name); ok {
		opts := []oaicompat.Option{oaicompat.WithName(name)}
		if key := config.ResolvePresetAPIKey(name, config.InferencePreset{}); key != "" {
			opts = append(opts, oaicompat.WithAPIKey(key))
		}
		return oaicompat.New(ep, opts...)
	}
	return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, google/gemini, ollama, ollama-cloud, llamacpp, vllm, groq, openrouter, deepseek, xai, mistral, cerebras, minimax, minimax-anthropic, litellm, lmstudio, or a configured preset)", name)
}

func noProviderConfiguredError() error {
	return errors.New("no provider configured and no local inference runner detected — " +
		"set defaults.provider in config (e.g. 'anthropic', 'openai', 'ollama', 'lmstudio') " +
		"or start a local server (ollama serve / llama-server / lmstudio / vllm)")
}

// detectLocalFallback probes bundled local runners (+ user-configured
// localhost presets) concurrently with a short budget. Returns the
// first reachable OAI-compat provider with at least one runnable model.
// Returns nil when no local runner can accept a chat request; the caller
// then reports the no-provider-configured setup error.
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
			models := picked.RunnableModels()
			tuiTrace("local fallback picked",
				"provider", picked.Name,
				"endpoint", picked.Endpoint,
				"models", len(models))
			span.SetAttributes(
				attribute.String("provider.name", picked.Name),
				attribute.String("provider.endpoint", picked.Endpoint),
				attribute.Int("provider.models", len(models)),
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
	for i := range results {
		r := &results[i]
		if !r.Reachable {
			continue
		}
		if len(r.RunnableModels()) > 0 {
			return r
		}
	}
	return nil
}

// builtinPreset returns (endpoint, api-key-env-var, ok) for bundled
// OAI-compat providers. API-key envs follow upstream convention so existing
// tooling keeps working.
func builtinPreset(name string) (string, string, bool) {
	return config.BuiltinInferencePreset(name)
}

func loadTheme(cfg *config.Config) (*theme.Theme, error) {
	if cfg != nil && strings.TrimSpace(cfg.TUI.Theme) != "" {
		return theme.Named(cfg.TUI.Theme)
	}
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
