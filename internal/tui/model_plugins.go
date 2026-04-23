package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// emitTurnCompleteToBridges pushes a JSON-encoded
// `{"kind":"turn_complete","turn":N}` event onto every background
// plugin's SessionBridge event channel. Plugins polling via
// stado_session_next_event will pop these and can trigger behaviour
// at turn boundaries. Buffer-full drops are tolerated (the bridge
// Emit is non-blocking).
func (m *Model) emitTurnCompleteToBridges() {
	if len(m.backgroundPlugins) == 0 {
		return
	}
	turn := 0
	if m.session != nil {
		turn = m.session.Turn()
	}
	payload := []byte(fmt.Sprintf(`{"kind":"turn_complete","turn":%d}`, turn))
	for _, bp := range m.backgroundPlugins {
		if bp.Host != nil {
			if bridge, ok := bp.Host.SessionBridge.(*pluginRuntime.SessionBridgeImpl); ok && bridge != nil {
				bridge.Emit(payload)
			}
		}
	}
}

// LoadBackgroundPlugins instantiates every plugin listed in
// cfg.Plugins.Background as a persistent (tick-every-turn) plugin.
// Each plugin's manifest is verified against the trust store + wasm
// digest before instantiation — same gate as `stado plugin run`. A
// single failing plugin surfaces as a system block in the TUI but
// doesn't abort the others. No-op when cfg.Plugins.Background is
// empty.
func (m *Model) LoadBackgroundPlugins(cfg *config.Config) {
	if len(cfg.Plugins.Background) == 0 {
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
	for _, id := range cfg.Plugins.Background {
		bp, note := m.loadOneBackground(ctx, rt, pluginsRoot, id)
		if note != "" {
			m.appendBlock(block{kind: "system", body: note})
		}
		if bp != nil {
			m.backgroundPlugins = append(m.backgroundPlugins, bp)
		}
	}
}

// loadOneBackground reads + verifies + instantiates a single plugin.
// Returns (plugin, advisory) — advisory is non-empty on load
// failure OR on successful load so the user knows the plugin is
// active. nil plugin signals "skip this one."
func (m *Model) loadOneBackground(ctx context.Context, rt *pluginRuntime.Runtime, pluginsRoot, id string) (*pluginRuntime.BackgroundPlugin, string) {
	dir := filepath.Join(pluginsRoot, id)
	mf, sig, err := plugins.LoadFromDir(dir)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: manifest load failed: %v", id, err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := plugins.VerifyWASMDigest(mf.WASMSHA256, wasmPath); err != nil {
		return nil, fmt.Sprintf("background plugin %s: digest mismatch: %v", id, err)
	}
	cfg, _ := config.Load()
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
	if bridge := m.buildPluginBridge(mf.Name); bridge != nil {
		host.SessionBridge = bridge
	}
	bp, err := pluginRuntime.LoadBackgroundPlugin(ctx, rt, wasmBytes, host)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: load: %v", id, err)
	}
	return bp, fmt.Sprintf("background plugin %s loaded (ticking on every turn boundary)", id)
}

// tickBackgroundPlugins invokes stado_plugin_tick on every loaded
// background plugin. Returns a tea.Cmd because the ticks run in a
// goroutine so a slow plugin can't freeze the UI. Called on each
// streamDoneMsg in Update. Plugins returning non-zero are dropped
// from the active set for the rest of the session.
func (m *Model) tickBackgroundPlugins() tea.Cmd {
	if len(m.backgroundPlugins) == 0 {
		return nil
	}
	if m.backgroundTickRunning {
		m.backgroundTickQueued = true
		return nil
	}
	m.backgroundTickRunning = true
	active := m.backgroundPlugins
	return func() tea.Msg {
		survivors := active[:0:len(active)]
		for _, bp := range active {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			unregister, err := bp.Tick(ctx)
			cancel()
			if err != nil || unregister {
				_ = bp.Close(context.Background())
				continue
			}
			survivors = append(survivors, bp)
		}
		return backgroundTickResultMsg{survivors: survivors}
	}
}

// backgroundTickResultMsg carries the post-tick surviving plugin
// list back to the UI goroutine so the assignment to m.backgroundPlugins
// happens under the bubbletea event loop rather than racing with it.
type backgroundTickResultMsg struct {
	survivors []*pluginRuntime.BackgroundPlugin
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
}

func (h hostAdapter) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h hostAdapter) Workdir() string        { return h.workdir }
func (h hostAdapter) Runner() sandbox.Runner { return h.runner }

func (h hostAdapter) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	return h.approval.RequestApproval(ctx, title, body)
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

// pluginForkAt returns a ForkFn closure that drives the same
// fork-from-point primitive `stado session fork --at` uses: resolve
// at_turn_ref against the parent session's refs, create a new session
// rooted at that commit, materialise the worktree, then write a
// trace-ref marker to the new session tagged with `Plugin: <name>`
// whose Summary is the plugin-provided seed message. Returns the new
// session ID so the plugin can surface it.
//
// DESIGN invariant: the parent session is never modified. The child
// carries a marker commit that records what the plugin summarised;
// when conversation persistence lands, that same marker seeds the
// child's m.msgs with the summary as its first user turn.
//
// Also posts a pluginForkMsg so the TUI update loop can render a
// user-visible notification (DESIGN invariant 4 — "user-visible by
// default").
func (m *Model) pluginForkAt(pluginName string) func(ctx context.Context, atTurnRef, seed string) (string, error) {
	return func(ctx context.Context, atTurnRef, seed string) (string, error) {
		if m.session == nil || m.session.Sidecar == nil {
			return "", fmt.Errorf("plugin fork: no live session")
		}
		sc := m.session.Sidecar
		parentID := m.session.ID

		// Resolve the fork point. Empty atTurnRef → parent's tree HEAD
		// (fork from the current state, same as `stado session fork`
		// without --at).
		var rootCommit plumbing.Hash
		if atTurnRef != "" {
			h, err := resolveTurnRefForBridge(sc, parentID, atTurnRef)
			if err != nil {
				return "", fmt.Errorf("plugin fork: resolve %s: %w", atTurnRef, err)
			}
			rootCommit = h
		} else {
			h, err := sc.ResolveRef(stadogit.TreeRef(parentID))
			if err == nil {
				rootCommit = h
			}
		}

		worktreeRoot := filepath.Dir(m.session.WorktreePath)
		childID := uuid.New().String()
		childSess, err := stadogit.CreateSession(sc, worktreeRoot, childID, rootCommit)
		if err != nil {
			return "", fmt.Errorf("plugin fork: create child: %w", err)
		}

		// Materialise the worktree at the fork point so the child is
		// a working session, not just a ref graph node.
		if !rootCommit.IsZero() {
			treeHash, tErr := childSess.TreeFromCommit(rootCommit)
			if tErr == nil {
				_ = childSess.MaterializeTreeToDir(treeHash, childSess.WorktreePath)
			}
		}

		// Write the Plugin: tagged seed marker onto the child's trace
		// ref. Best-effort — the fork already succeeded; commit
		// failures shouldn't invalidate the new session.
		_, _ = childSess.CommitToTrace(stadogit.CommitMeta{
			Tool:     "plugin_fork",
			ShortArg: atTurnRef,
			Summary:  trimSeed(seed, 60),
			Agent:    "plugin:" + pluginName,
			Plugin:   pluginName,
			Turn:     0,
		})

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

// resolveTurnRefForBridge is the bridge-local equivalent of
// cmd/stado's resolveTurnRef. Inlined to avoid importing cmd/stado
// from internal/tui.
func resolveTurnRefForBridge(sc *stadogit.Sidecar, srcID, target string) (plumbing.Hash, error) {
	if strings.HasPrefix(target, "turns/") {
		return sc.ResolveRef(plumbing.ReferenceName("refs/sessions/" + srcID + "/" + target))
	}
	if len(target) < 40 {
		return plumbing.ZeroHash, fmt.Errorf("pass a full 40-char commit sha or turns/<N>, got %q", target)
	}
	return plumbing.NewHash(target), nil
}

// runPluginToolAsync spawns a fresh wazero runtime, instantiates the
// module under its capability-bound Host, invokes the tool, and posts
// the outcome back via pluginRunResultMsg. Hard-capped at 30s so a
// runaway plugin can't wedge the TUI.
func runPluginToolAsync(dir string, mf *plugins.Manifest, tdef plugins.ToolDef, argsJSON, pluginID string, bridge *pluginRuntime.SessionBridgeImpl, approval pluginRuntime.ApprovalBridge) tea.Cmd {
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

// renderInstalledPluginList scans pluginsRoot and returns a human body
// enumerating each installed plugin with the tools it declares. Helpful
// discovery block for the bare `/plugin` command.
func renderInstalledPluginList(pluginsRoot string) string {
	entries, err := os.ReadDir(pluginsRoot)
	if err != nil || len(entries) == 0 {
		return "No plugins installed. Run `stado plugin install <dir>` to add one, or see examples/plugins/hello/."
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
