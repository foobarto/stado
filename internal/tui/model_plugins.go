package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// LoadBackgroundPlugins instantiates every plugin listed in
// cfg.Plugins.Background plus the default bundled auto-compact plugin
// as persistent (tickable) plugins. A single failing plugin surfaces
// as a system block in the TUI but doesn't abort the others.
func (m *Model) LoadBackgroundPlugins(cfg *config.Config) {
	ids := effectiveBackgroundPluginIDs(cfg)
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

	pluginsRoot := filepath.Join(cfg.StateDir(), "plugins")
	for _, id := range ids {
		bp, note := m.loadOneBackground(ctx, rt, cfg, pluginsRoot, id)
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

func effectiveBackgroundPluginIDs(cfg *config.Config) []string {
	if cfg == nil {
		return bundledplugins.DefaultBackgroundPlugins()
	}
	var ids []string
	seen := map[string]struct{}{}
	for _, id := range bundledplugins.DefaultBackgroundPlugins() {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, id := range cfg.Plugins.Background {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// loadOneBackground reads + verifies + instantiates a single plugin.
// Returns (plugin, advisory) — advisory is non-empty on load
// failure OR on successful load so the user knows the plugin is
// active. nil plugin signals "skip this one."
func (m *Model) loadOneBackground(ctx context.Context, rt *pluginRuntime.Runtime, cfg *config.Config, pluginsRoot, id string) (*pluginRuntime.BackgroundPlugin, string) {
	if bundled, ok := bundledplugins.LookupBackgroundPlugin(id); ok {
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

	dir := filepath.Join(pluginsRoot, id)
	mf, sig, err := plugins.LoadFromDir(dir)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: manifest load failed: %v", id, err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := plugins.VerifyWASMDigest(mf.WASMSHA256, wasmPath); err != nil {
		return nil, fmt.Sprintf("background plugin %s: digest mismatch: %v", id, err)
	}
	if cfg != nil {
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(mf, sig); err != nil {
			return nil, fmt.Sprintf("background plugin %s: signature: %v", id, err)
		}
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: read wasm: %v", id, err)
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
	spawn    func(context.Context, subagent.Request) (subagent.Result, error)
}

func (h hostAdapter) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h hostAdapter) Workdir() string        { return h.workdir }
func (h hostAdapter) Runner() sandbox.Runner { return h.runner }

func (h hostAdapter) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	return h.approval.RequestApproval(ctx, title, body)
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

// buildPluginBridge wires the live TUI's Session + active provider
// behind a SessionBridgeImpl so plugins that declared session/LLM
// capabilities see real conversation state. Returns nil when the TUI
// has no session or provider — plugins with those capabilities will
// error cleanly at call time, matching the `stado plugin run` CLI
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
	exec, err := runtime.BuildExecutor(child, cfg, "stado-tui")
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
	m.executor = exec
	m.cwd = child.WorktreePath
	m.msgs = nil
	m.blocks = nil
	m.todos = nil
	m.queuedPrompt = ""
	m.pendingCalls = nil
	m.pendingResults = nil
	m.turnToolCalls = nil
	m.usage = agent.Usage{}
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
func runPluginToolAsync(cfg *config.Config, dir string, mf *plugins.Manifest, tdef plugins.ToolDef, argsJSON, pluginID string, bridge *pluginRuntime.SessionBridgeImpl, approval pluginRuntime.ApprovalBridge) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		wasmBytes, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: err.Error()}
		}
		rt, err := pluginRuntime.New(ctx)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "runtime: " + err.Error()}
		}
		defer func() { _ = rt.Close(ctx) }()

		host := pluginRuntime.NewHost(*mf, dir, nil)
		host.ApprovalBridge = approval
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

// renderInstalledPluginList scans pluginsRoot and returns a human body
// enumerating each installed plugin with the tools it declares. Helpful
// discovery block for the bare `/plugin` command.
func renderInstalledPluginList(pluginsRoot string) string {
	entries, err := os.ReadDir(pluginsRoot)
	if err != nil || len(entries) == 0 {
		return "No plugins installed. Run `stado plugin install <dir>` to add one, or see plugins/examples/hello/."
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return "No plugins installed."
	}
	sort.Strings(dirs)

	var sb strings.Builder
	sb.WriteString("Installed plugins:")
	for _, name := range dirs {
		sb.WriteString("\n  /plugin:" + name)
		mf, _, err := plugins.LoadFromDir(filepath.Join(pluginsRoot, name))
		if err != nil {
			sb.WriteString("  (manifest load failed: " + err.Error() + ")")
			continue
		}
		for _, t := range mf.Tools {
			sb.WriteString("\n    · " + t.Name)
			if t.Description != "" {
				sb.WriteString(" — " + t.Description)
			}
		}
	}
	sb.WriteString("\n\nRun a tool with  /plugin:<name> <tool> [json-args]")
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
			sb.WriteString("\n      " + t.Description)
		}
	}
	sb.WriteString(fmt.Sprintf("\n\nRun with  /plugin:%s <tool> [json-args]", nameVer))
	return sb.String()
}
