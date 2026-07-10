package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// LoadBackgroundPlugins instantiates every plugin listed in
// cfg.Plugins.Background plus the default bundled auto-compact plugin
// as persistent (tickable) plugins. A single failing plugin surfaces
// as a system block in the TUI but doesn't abort the others.
func (m *Model) LoadBackgroundPlugins(cfg *config.Config) {
	// Per-persona plugins (2026-06-13): the active-at-launch persona's
	// `plugins:` are added to the background set. LAUNCH-ONLY — a live
	// /persona switch does NOT start/stop background plugins (the lifecycle
	// is session-start-only; LoadBackgroundPlugins runs once at boot, after
	// initPersona). A nil persona contributes nothing.
	var personaPlugins []string
	if m.persona != nil {
		personaPlugins = m.persona.Plugins
	}
	ids := effectiveBackgroundPluginIDs(cfg, personaPlugins)
	if len(ids) == 0 {
		return
	}
	ctx := m.rootCtx
	if ctx == nil {
		ctx = context.Background()
	}
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "background plugins: runtime: " + err.Error()})
		return
	}
	m.bgPluginRuntime = rt

	pluginRoots := cfg.AllPluginDirs() // EP-0035: global + project .stado/plugins/
	for _, id := range ids {
		bp, note := m.loadOneBackground(ctx, rt, cfg, pluginRoots, id)
		if note != "" {
			if bp != nil {
				slog.Info(note)
			} else {
				m.recordBackgroundPluginIssue(note)
				m.appendBlock(block{kind: "system", body: note})
			}
		}
		if bp != nil {
			m.backgroundPlugins = append(m.backgroundPlugins, bp)
		}
	}
}

// effectiveBackgroundPluginIDs is the launch-time background-plugin set:
// bundled defaults, then cfg.Plugins.Background, then the active-at-launch
// persona's `plugins:` — unioned and deduped, in that precedence order.
// personaPlugins is launch-only (a live /persona switch never re-runs this).
func effectiveBackgroundPluginIDs(cfg *config.Config, personaPlugins []string) []string {
	var ids []string
	seen := map[string]struct{}{}
	add := func(list []string) {
		for _, id := range list {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	add(runtime.DefaultBackgroundPlugins())
	if cfg != nil {
		add(cfg.Plugins.Background)
	}
	add(personaPlugins)
	return ids
}

// loadOneBackground reads + verifies + instantiates a single plugin.
// Returns (plugin, advisory) — advisory is non-empty on load
// failure OR on successful load so the user knows the plugin is
// active. nil plugin signals "skip this one."
func (m *Model) loadOneBackground(ctx context.Context, rt *pluginRuntime.Runtime, cfg *config.Config, pluginRoots []string, id string) (*pluginRuntime.BackgroundPlugin, string) {
	if bundled, ok := runtime.LookupBackgroundPlugin(id); ok {
		host := pluginRuntime.NewHost(bundled.Manifest, "", nil)
		host.ApprovalBridge = tuiApprovalBridge{model: m}
		attachMemoryBridge(cfg, host, bundled.Manifest.Name)
		if bridge := m.buildPluginBridge(bundled.Manifest.Name); bridge != nil {
			host.SessionBridge = bridge
		}
		bp, err := pluginRuntime.LoadBackgroundPlugin(ctx, rt, bundled.WASM, host)
		if err != nil {
			return nil, fmt.Sprintf("background plugin %s: load: %v", bundled.ID, err)
		}
		return bp, fmt.Sprintf("background plugin %s loaded (bundled default)", bundled.ID)
	}

	dir, err := plugins.InstalledDirInAny(pluginRoots, id)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: %v", id, err)
	}
	mf, sig, err := plugins.LoadFromDir(dir)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: manifest load failed: %v", id, err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	wasmBytes, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: digest mismatch: %v", id, err)
	}
	if cfg != nil {
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(mf, sig); err != nil {
			return nil, fmt.Sprintf("background plugin %s: signature: %v", id, err)
		}
	}
	host := pluginRuntime.NewHost(*mf, dir, nil)
	host.ApprovalBridge = tuiApprovalBridge{model: m}
	attachMemoryBridge(cfg, host, mf.Name)
	if bridge := m.buildPluginBridge(mf.Name); bridge != nil {
		host.SessionBridge = bridge
	}
	bp, err := pluginRuntime.LoadBackgroundPlugin(ctx, rt, wasmBytes, host)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: load: %v", id, err)
	}
	return bp, fmt.Sprintf("background plugin %s loaded (ticking on every turn boundary)", id)
}

// tickBackgroundPluginsWithEvent pushes one event onto every loaded
// background plugin's bridge, refreshes the bridge to the current
// session snapshot, then invokes stado_plugin_tick. Returns a tea.Cmd
// because the ticks run in a goroutine so a slow plugin can't freeze
// the UI. Plugins returning non-zero are dropped from the active set.
func (m *Model) tickBackgroundPluginsWithEvent(payload []byte) tea.Cmd {
	if len(m.backgroundPlugins) == 0 {
		return nil
	}
	if m.backgroundTickRunning {
		m.backgroundTickQueued = true
		m.backgroundTickPayload = append([]byte(nil), payload...)
		return nil
	}
	m.backgroundTickRunning = true
	active := m.backgroundPlugins
	return func() tea.Msg {
		survivors := active[:0:len(active)]
		var issues []string
		for _, bp := range active {
			if bp.Host != nil {
				if bridge := m.buildPluginBridge(bp.Name()); bridge != nil {
					bp.Host.SessionBridge = bridge
					bridge.Emit(payload)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			unregister, err := bp.Tick(ctx)
			cancel()
			if err != nil || unregister {
				name := "plugin"
				if bp != nil && strings.TrimSpace(bp.Name()) != "" {
					name = bp.Name()
				}
				if err != nil {
					issues = append(issues, fmt.Sprintf("%s tick: %v", name, err))
				} else {
					issues = append(issues, fmt.Sprintf("%s unregistered itself during tick", name))
				}
				_ = bp.Close(context.Background())
				continue
			}
			survivors = append(survivors, bp)
		}
		return backgroundTickResultMsg{survivors: survivors, issues: issues}
	}
}

func (m *Model) closeBackgroundPlugins(ctx context.Context) {
	for _, bp := range m.backgroundPlugins {
		_ = bp.Close(ctx)
	}
	m.backgroundPlugins = nil
	if m.bgPluginRuntime != nil {
		_ = m.bgPluginRuntime.Close(ctx)
		m.bgPluginRuntime = nil
	}
	// Reap any PTYs opened by bundled shell.* / pty.* tools during
	// the session — without this they'd outlive the TUI process.
	if m.ptyManager != nil {
		m.ptyManager.CloseAll()
		m.ptyManager = nil
	}
	// Reap any LSP servers the session launched (post-edit diagnostics /
	// the *ViaManager tool seams) — without this gopls / pyright would
	// outlive the TUI process.
	if m.lspManager != nil {
		m.lspManager.CloseAll()
		m.lspManager = nil
	}
}

// backgroundTickResultMsg carries the post-tick surviving plugin
// list back to the UI goroutine so the assignment to m.backgroundPlugins
// happens under the bubbletea event loop rather than racing with it.
type backgroundTickResultMsg struct {
	survivors []*pluginRuntime.BackgroundPlugin
	issues    []string
}

func (m *Model) recordBackgroundPluginIssue(issue string) {
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return
	}
	m.backgroundPluginIssues = append(m.backgroundPluginIssues, issue)
	const maxBackgroundPluginIssues = 3
	if len(m.backgroundPluginIssues) > maxBackgroundPluginIssues {
		m.backgroundPluginIssues = append([]string(nil), m.backgroundPluginIssues[len(m.backgroundPluginIssues)-maxBackgroundPluginIssues:]...)
	}
}

type tuiApprovalBridge struct {
	model *Model
}

func (b tuiApprovalBridge) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	if b.model == nil {
		return false, errors.New("approval UI unavailable")
	}
	return b.model.requestPluginApproval(ctx, title, body)
}

// tuiChoiceBridge implements pluginRuntime.ChoiceBridge over the TUI
// model's drawer. Single-flight: the bridge serialises requests via
// the model's pluginChoiceRequestMsg path, and the model rejects a
// second concurrent request before it ever lands here. Q3.
type tuiChoiceBridge struct {
	model *Model
}

func (b tuiChoiceBridge) RequestChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	if b.model == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("choice UI unavailable")
	}
	return b.model.requestPluginChoice(ctx, req)
}

// tuiPrintBridge implements pluginRuntime.PrintBridge over the TUI
// model. Fire-and-forget: posts a pluginPrintMsg into the program
// loop and returns nil. The model's Update handler appends a
// system-styled block carrying the text + severity. Drop-on-floor
// when m.program is nil so test models without a live tea.Program
// don't deadlock waiting on a sendMsg into a missing program.
// F9a.
type tuiPrintBridge struct {
	model *Model
}

func (b tuiPrintBridge) Print(_ context.Context, text string, opts pluginRuntime.PrintOpts) error {
	if b.model == nil || b.model.program == nil {
		return nil
	}
	b.model.sendMsg(pluginPrintMsg{text: text, opts: opts})
	return nil
}

// tuiRenderBridge implements pluginRuntime.RenderBridge over the TUI
// model. Fire-and-forget: posts a pluginRenderMsg carrying the
// decoded Panel into the program loop and returns nil. The model's
// Update handler renders the panel to ASCII (bordered per body kind)
// and appends it as a system block. Same drop-on-nil-program guard
// the print bridge uses, for the same reason. Spec: F9b.2
// (.agent/specs/open/f9b-ui-render.md).
type tuiRenderBridge struct {
	model *Model
}

func (b tuiRenderBridge) Render(_ context.Context, panel pluginRuntime.Panel) error {
	if b.model == nil || b.model.program == nil {
		return nil
	}
	b.model.sendMsg(pluginRenderMsg{panel: panel})
	return nil
}

func (m *Model) turnCompleteEvent() []byte {
	turn := 0
	if m.session != nil {
		turn = m.session.Turn()
	}
	return []byte(fmt.Sprintf(`{"kind":"turn_complete","turn":%d}`, turn))
}

func (m *Model) contextOverflowEvent(prompt string) []byte {
	return []byte(fmt.Sprintf(
		`{"kind":"context_overflow","turn":%d,"prompt":%q,"fraction":%.4f,"hard_threshold":%.4f}`,
		m.currentTurnNumber(), prompt, m.contextFraction(), m.ctxHardThreshold,
	))
}

func (m *Model) currentTurnNumber() int {
	if m.session == nil {
		return 0
	}
	return m.session.Turn()
}

func (m *Model) hasAutoCompactBackgroundPlugin() bool {
	for _, bp := range m.backgroundPlugins {
		if bp != nil && bp.Name() == "auto-compact" {
			return true
		}
	}
	return false
}

// hostAdapter implements tool.Host for the executor goroutine. Tool calls
// themselves are yolo by default; the adapter only exposes an explicit
// approval bridge for plugins that request it.
// readLog delegates PriorRead/RecordRead to the Executor's shared log so
// the read tool can dedup across a session's turns.
type hostAdapter struct {
	workdir  string
	readLog  *tools.ReadLog
	runner   sandbox.Runner
	approval tuiApprovalBridge
	choice   tuiChoiceBridge
	print    tuiPrintBridge  // F9a — stado_ui_print routing
	render   tuiRenderBridge // F9b.2 — stado_ui_render routing
	spawn    func(context.Context, subagent.Request) (subagent.Result, error)
	// activate is the lazy-load activation hook. Called by the
	// tools__describe / tools__activate / plugin__load meta-tools via
	// the pkg/tool.ToolActivator interface; adds the named tool to
	// this session's activated set so subsequent toolDefs() include it.
	// EP-0037.
	activate func(name string)
	// deactivate is the inverse hook for tools__deactivate /
	// plugin__unload via pkg/tool.ToolDeactivator.
	deactivate func(name string)
	// progress receives stado_progress emissions from bundled wasm
	// plugins. Wired so the TUI surfaces progress lines in the
	// sidebar log tail. EP-0038h.
	progress        func(plugin, text string)
	executorSandbox runtime.ExecutorSandbox

	// pty is the TUI-session-lifetime PTY manager shared across every
	// bundled-plugin tool dispatch. Without this each call would
	// build a fresh pluginRuntime with its own pty.NewManager(), and
	// shell.spawn → shell.read/write across calls would fail with
	// "session not found." Bug-fix per operator report.
	pty *pty.Manager
}

func (h hostAdapter) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h hostAdapter) Workdir() string        { return h.workdir }
func (h hostAdapter) Runner() sandbox.Runner { return h.runner }

func (h hostAdapter) DefaultSandboxPolicy() any {
	return h.executorSandbox.DefaultSandboxPolicy(h.workdir)
}

func (h hostAdapter) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	return h.approval.RequestApproval(ctx, title, body)
}

func (h hostAdapter) RequestChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	return h.choice.RequestChoice(ctx, req)
}

// Print implements pluginRuntime.PrintBridge so the host adapter
// satisfies the interface assertion in attachLifecycleBridges,
// which wires the print bridge onto the per-plugin runtime host.
// Routes through tuiPrintBridge → pluginPrintMsg → Update handler.
// F9a.
func (h hostAdapter) Print(ctx context.Context, text string, opts pluginRuntime.PrintOpts) error {
	return h.print.Print(ctx, text, opts)
}

// Render implements pluginRuntime.RenderBridge so attachLifecycleBridges
// wires the render bridge onto the per-plugin runtime host. Routes
// through tuiRenderBridge → pluginRenderMsg → Update handler. F9b.2.
func (h hostAdapter) Render(ctx context.Context, panel pluginRuntime.Panel) error {
	return h.render.Render(ctx, panel)
}

func (h hostAdapter) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	if h.spawn == nil {
		return subagent.Result{}, errors.New("spawn_agent unavailable: current host does not support subagents")
	}
	return h.spawn(ctx, req)
}

func (h hostAdapter) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	if h.readLog == nil {
		return tool.PriorReadInfo{}, false
	}
	return h.readLog.PriorRead(key)
}

func (h hostAdapter) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	if h.readLog == nil {
		return
	}
	h.readLog.RecordRead(key, info)
}

// ActivateTool implements pkg/tool.ToolActivator — surfaces a named tool
// into the next turn's tool surface. Called by tools__describe /
// tools__activate / plugin__load. When the activate hook isn't wired
// (no Model context, e.g. tool run), the activation is silently dropped.
func (h hostAdapter) ActivateTool(name string) {
	if h.activate != nil {
		h.activate(name)
	}
}

// DeactivateTool implements pkg/tool.ToolDeactivator — removes a tool
// from this session's per-turn surface. Inverse of ActivateTool.
func (h hostAdapter) DeactivateTool(name string) {
	if h.deactivate != nil {
		h.deactivate(name)
	}
}

// EmitProgress implements pkg/tool.ProgressEmitter — surfaces
// stado_progress payloads to the TUI's sidebar log tail. EP-0038h.
func (h hostAdapter) EmitProgress(plugin, text string) {
	if h.progress != nil {
		h.progress(plugin, text)
	}
}

// PTYManager implements pkg/tool.PTYProvider — bundled shell.* /
// pty.* tools reuse the TUI-session-lifetime manager via this hook
// so session ids survive across consecutive tool dispatches.
func (h hostAdapter) PTYManager() any { return h.pty }

// buildPluginBridge wires the live TUI's Session + active provider
// behind a SessionBridgeImpl so plugins that declared session/LLM
// capabilities see real conversation state. Returns nil when the TUI
// has no session or provider — plugins with those capabilities will
// error cleanly at call time, matching the `stado tool run` CLI
// path's behaviour. `pluginName` populates the `Plugin:` audit
// trailer so plugin-initiated LLM calls + forks are attributable in
// the trace log.
func (m *Model) buildPluginBridge(pluginName string) *pluginRuntime.SessionBridgeImpl {
	if m.session == nil && m.provider == nil {
		return nil
	}
	msgs := append([]agent.Message(nil), m.msgs...) // snapshot by copy
	bridge := pluginRuntime.NewSessionBridge(m.session, m.provider, m.model)
	bridge.PluginName = pluginName
	bridge.MessagesFn = func() []agent.Message { return msgs }
	bridge.TokensFn = func() int { return m.usage.InputTokens }
	if m.session != nil {
		bridge.LastTurnRef = func() string {
			return string(stadogit.TurnTagRef(m.session.ID, m.session.Turn()))
		}
		bridge.ForkFn = m.pluginForkAt(pluginName)
	}
	return bridge
}

func (m *Model) adoptForkedSession(childID, seed string) tea.Cmd {
	if m.session == nil || m.session.Sidecar == nil {
		m.appendBlock(block{kind: "system", body: "auto-recovery forked a child session, but no live parent session is attached"})
		m.renderBlocks()
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		m.appendBlock(block{kind: "system", body: "auto-recovery: config load: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	child, err := stadogit.OpenSession(m.session.Sidecar, filepath.Dir(m.session.WorktreePath), childID)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "auto-recovery: open child session: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	exec, err := runtime.BuildExecutorQuiet(child, cfg, "stado-tui")
	if err != nil {
		m.appendBlock(block{kind: "system", body: "auto-recovery: executor: " + err.Error()})
		m.renderBlocks()
		return nil
	}

	prompt := m.recoveryPrompt
	m.recoveryPrompt = ""
	m.recoveryPluginName = ""
	m.recoveryPluginActive = false
	m.session = child
	m.executorSandbox.Apply(exec)
	m.executor = exec
	m.msgs = nil
	m.blocks = nil
	m.todos = nil
	m.queuedPrompt = ""
	m.steeringMsg = "" // #16: don't carry a parent-turn steer into the child session
	m.pendingCalls = nil
	m.pendingResults = nil
	m.turnToolCalls = nil
	m.usage = agent.Usage{}
	m.cumulativeInputTokens = 0
	m.budgetWarned = false
	m.budgetAcked = false
	m.state = stateIdle
	m.LoadPersistedConversation()
	if res, err := instructions.Load(m.cwd); err == nil {
		m.systemPrompt = res.Content
		m.systemPromptPath = res.Path
	} else {
		m.systemPrompt = ""
		m.systemPromptPath = ""
	}
	if sks, err := skills.Load(m.cwd); err == nil {
		m.skills = sks
	} else {
		m.skills = nil
	}
	// Re-derive skill slash shortcuts for the recovered session's cwd so
	// the palette + dispatch map track the new skill set. Warnings go to a
	// system block since the TUI is live.
	m.registerSkillSlashCommands(func(msg string) {
		m.appendBlock(block{kind: "system", body: msg})
	})

	body := fmt.Sprintf("auto-recovery: switched to compacted child session %s", childID)
	if strings.TrimSpace(seed) != "" {
		body += "\nsummary: " + trimSeed(seed, 120)
	}
	if prompt == "" {
		m.appendBlock(block{kind: "system", body: body})
		m.renderBlocks()
		return nil
	}
	m.appendBlock(block{kind: "system", body: body + "\nreplaying blocked prompt in the child session"})
	m.appendUser(prompt)
	m.input.Reset()
	m.renderBlocks()
	return m.startStream()
}

// pluginForkAt returns a ForkFn closure that drives the same
// fork-from-point primitive `stado session fork --at` uses: resolve
// at_turn_ref against the parent session's refs, create a new session
// rooted at that commit, materialise the worktree, then write a
// trace-ref marker to the new session tagged with `Plugin: <name>`
// whose Summary is the plugin-provided seed message. Returns the new
// session ID so the plugin can surface it.
//
// DESIGN invariant: the parent session is never modified. The child
// carries a trace marker commit that records what the plugin
// summarised, and the child's persisted conversation is seeded with
// that summary as its first user turn.
//
// Also posts a pluginForkMsg so the TUI update loop can render a
// user-visible notification (DESIGN invariant 4 — "user-visible by
// default").
func (m *Model) pluginForkAt(pluginName string) func(ctx context.Context, atTurnRef, seed string) (string, error) {
	return func(ctx context.Context, atTurnRef, seed string) (string, error) {
		if m.session == nil {
			return "", fmt.Errorf("plugin fork: no live session")
		}
		cfg, err := config.Load()
		if err != nil {
			return "", fmt.Errorf("plugin fork: load config: %w", err)
		}
		childSess, err := runtime.ForkPluginSession(cfg, m.session, atTurnRef, seed, pluginName)
		if err != nil {
			return "", err
		}
		childID := childSess.ID

		// Notify the user asynchronously via the tea program. Not a
		// synchronous block — the plugin is waiting on this function
		// to return, and we don't want to sleep here waiting for the
		// UI. If the program isn't attached (test harness), the send
		// is a no-op.
		if m.program != nil {
			m.program.Send(pluginForkMsg{
				plugin:    pluginName,
				childID:   childID,
				atTurnRef: atTurnRef,
				seed:      seed,
			})
		}
		return childID, nil
	}
}

// runPluginToolAsync spawns a fresh wazero runtime, instantiates the
// module under its capability-bound Host, invokes the tool, and posts
// the outcome back via pluginRunResultMsg. Hard-capped at 30s so a
// runaway plugin can't wedge the TUI.
//
// `print` and `render` bridges are attached when non-nil so plugins
// declaring `ui:print` / `ui:render` from the /tool or /plugin: slash
// path see the operator's surface (F9a, F9b). Pre-fix these were
// only wired through the agent loop's hostAdapter — operator-driven
// invocations never saw the panel/print emit because pluginrun.Run
// uses attachLifecycleBridges to pick them off the host, and the
// host built here didn't carry them.
func runPluginToolAsync(cfg *config.Config, dir string, mf *plugins.Manifest, tdef plugins.ToolDef, argsJSON, pluginID string, wasmBytes []byte, bridge *pluginRuntime.SessionBridgeImpl, approval pluginRuntime.ApprovalBridge, print pluginRuntime.PrintBridge, render pluginRuntime.RenderBridge, choice pluginRuntime.ChoiceBridge) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: err.Error()}
		}

		rt, err := pluginRuntime.New(ctx)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "runtime: " + err.Error()}
		}
		defer func() { _ = rt.Close(ctx) }()

		host := pluginRuntime.NewHost(*mf, dir, nil)
		// EP-0029 D4: this operator-driven (/tool, /plugin:) path runs
		// the plugin directly rather than via pluginrun.Run, so populate
		// StateDir here too — otherwise a plugin declaring cfg:state_dir
		// invoked from the TUI would read an empty string.
		if cfg != nil {
			host.StateDir = cfg.StateDir()
		}
		host.ApprovalBridge = approval
		// F9a / F9b / Q3: attach print + render + choice bridges when
		// supplied, so plugins emitting via `stado_ui_print` /
		// `stado_ui_render` / `stado_ui_choose` from operator-driven
		// invocations (/tool, /plugin:) reach the TUI surface — not
		// just from agent-loop tool calls.
		if print != nil {
			host.PrintBridge = print
		}
		if render != nil {
			host.RenderBridge = render
		}
		if choice != nil {
			host.ChoiceBridge = choice
		}
		attachMemoryBridge(cfg, host, mf.Name)
		// Attach the session bridge only when the plugin declared at
		// least one session/LLM capability AND the caller supplied a
		// bridge. Keeps existing tool-only plugins (like the hello
		// example) on their existing code path.
		if bridge != nil && (host.SessionObserve || host.SessionRead || host.SessionFork || host.LLMInvokeBudget > 0) {
			host.SessionBridge = bridge
		}
		if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "host imports: " + err.Error()}
		}
		mod, err := rt.Instantiate(ctx, wasmBytes, *mf)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "instantiate: " + err.Error()}
		}
		defer func() { _ = mod.Close(ctx) }()

		pt, err := pluginRuntime.NewPluginTool(mod, tdef)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: err.Error()}
		}
		res, err := pt.Run(ctx, []byte(argsJSON), nil)
		if err != nil {
			msg := err.Error()
			if res.Error != "" {
				msg = res.Error
			}
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: msg}
		}
		if res.Error != "" {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: res.Error}
		}
		return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, content: res.Content}
	}
}

func attachMemoryBridge(cfg *config.Config, host *pluginRuntime.Host, pluginName string) {
	if cfg == nil || host == nil || !host.NeedsMemoryBridge() {
		return
	}
	host.MemoryBridge = pluginRuntime.NewLocalMemoryBridge(cfg.StateDir(), "plugin:"+pluginName)
}

// pluginToolIndent is the column the per-plugin tool list is indented to
// under each "/plugin:<name>" header, keeping the nested hierarchy.
const pluginToolIndent = 4

// pluginToolListWidth is the WrapDescList width for a plugin's nested
// tool block: the panel width minus the nesting indent. It must NOT
// exceed width-pluginToolIndent, or the indented lines overflow the
// enclosing system block, which re-wraps them and breaks the hanging
// indent (the same class of bug as slashListWidth's old floor). A
// too-wide floor would re-introduce that overflow at narrow viewports,
// so the result is floored at 1 (WrapDescList itself floors its inner
// descWidth at 1 and never panics) rather than clamped UP to a fixed
// minimum wider than the available space.
func pluginToolListWidth(width int) int {
	w := width - pluginToolIndent
	if w < 1 {
		w = 1
	}
	return w
}

// renderInstalledPluginList scans all pluginRoots and returns a human
// body enumerating each installed plugin with the tools it declares.
// Helpful discovery block for the bare `/plugin` command. EP-0035:
// scans all roots so project-local plugins are listed alongside global.
//
// width is the live panel width; each plugin's tools are rendered with
// render.WrapDescList (no truncation, hanging-indented descriptions) and
// then indented under the plugin header in the nested hierarchy.
func renderInstalledPluginList(width int, pluginRoots ...string) string {
	seen := map[string]struct{}{}
	var allDirs []string
	for _, root := range pluginRoots {
		ds, err := plugins.ListInstalledDirs(root)
		if err != nil {
			continue
		}
		for _, d := range ds {
			if _, already := seen[d]; !already {
				seen[d] = struct{}{}
				allDirs = append(allDirs, d)
			}
		}
	}
	if len(allDirs) == 0 {
		return "No plugins installed. Run `stado plugin install github.com/foobarto/stado-plugins/<plugin>@<version>` to add one."
	}
	pluginsRoot := pluginRoots[0]

	// Inner width for the tool descriptions: the panel minus the nesting
	// indent, never wider than the available space (see pluginToolListWidth).
	toolWidth := pluginToolListWidth(width)

	var sb strings.Builder
	sb.WriteString("Installed plugins:")
	for _, name := range allDirs {
		sb.WriteString("\n  /plugin:" + name)
		// Find the first root that has this plugin dir.
		var pluginPath string
		for _, root := range pluginRoots {
			p := filepath.Join(root, name)
			if _, err := os.Lstat(p); err == nil {
				pluginPath = p
				break
			}
		}
		if pluginPath == "" {
			pluginPath = filepath.Join(pluginsRoot, name)
		}
		mf, _, err := plugins.LoadFromDir(pluginPath)
		if err != nil {
			sb.WriteString("  (manifest load failed: " + err.Error() + ")")
			continue
		}
		if len(mf.Tools) == 0 {
			continue
		}
		// Full descriptions, no truncation: WrapDescList hangs continuation
		// lines at the gutter so a long body stays inside the nested block
		// instead of wrapping back to column 0. The "· " bullet rides in
		// the Name so it aligns in the gutter; the whole block is then
		// indented under the plugin header.
		rows := make([]render.DescRow, 0, len(mf.Tools))
		for _, t := range mf.Tools {
			rows = append(rows, render.DescRow{Name: "· " + t.Name, Desc: textutil.SanitizeForTerminal(t.Description)})
		}
		block := render.WrapDescList(rows, toolWidth)
		pad := strings.Repeat(" ", pluginToolIndent)
		for _, ln := range strings.Split(block, "\n") {
			sb.WriteString("\n" + pad + ln)
		}
	}
	sb.WriteString("\n\nRun a tool with  /plugin:<name> <tool> [json-args]")
	sb.WriteString("\nSee one plugin's full descriptions with  /plugin <name>")
	return sb.String()
}

// renderPluginTools formats one plugin's manifest tools for display
// when the user asks about it specifically.
func renderPluginTools(nameVer string, m *plugins.Manifest) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Plugin %s  (author=%s, caps=%d)", nameVer, m.Author, len(m.Capabilities)))
	if len(m.Tools) == 0 {
		sb.WriteString("\n  (no tools declared)")
		return sb.String()
	}
	sb.WriteString("\nTools:")
	for _, t := range m.Tools {
		sb.WriteString("\n  · " + t.Name)
		if t.Description != "" {
			sb.WriteString("\n      " + textutil.SanitizeForTerminal(t.Description))
		}
	}
	sb.WriteString(fmt.Sprintf("\n\nRun with  /plugin:%s <tool> [json-args]", nameVer))
	return sb.String()
}
