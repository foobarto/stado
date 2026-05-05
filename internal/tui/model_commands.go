package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/integrations"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tui/modelpicker"
	"github.com/foobarto/stado/pkg/agent"
)

// shortFleetID returns the first 8 chars of a fleet id for stderr/
// system-block display. Mirrors the picker's truncation so user logs
// match the modal.
func shortFleetID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// spawnerFunc adapts a (ctx, req)→(result, error) closure to the
// runtime.Spawner interface that Fleet.Spawn expects.
type spawnerFunc func(ctx context.Context, req subagent.Request) (subagent.Result, error)

func (f spawnerFunc) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	return f(ctx, req)
}

// spawnFleetAgent fires a background agent via the runtime Fleet,
// using the same SubagentRunner the spawn_agent tool uses. Returns
// a Cmd so the caller can chain it like any other slash command.
func (m *Model) spawnFleetAgent(prompt string) tea.Cmd {
	if m.session == nil {
		m.appendBlock(block{kind: "system", body: "spawn: no active session — start a turn first so a parent session exists"})
		return nil
	}
	if m.fleet == nil {
		m.fleet = runtime.NewFleet()
	}
	spawner := spawnerFunc(m.buildSubagentSpawner())
	id, err := m.fleet.Spawn(m.rootCtx, spawner, prompt, runtime.SpawnOptions{
		Provider: m.providerDisplayName(),
		Model:    m.model,
	})
	if err != nil {
		m.appendBlock(block{kind: "system", body: "spawn: " + err.Error()})
		return nil
	}
	short := id
	if len(short) >= 8 {
		short = short[:8]
	}
	m.appendBlock(block{kind: "system",
		body: "spawned background agent " + short + " — `/fleet` to view"})
	m.renderBlocks()
	return nil
}

// anyModalOpen returns true when any modal picker / overlay is
// visible. Source for the Ctrl+C "close popup" route at the top of
// Update.
func (m *Model) anyModalOpen() bool {
	switch {
	case m.modelPicker.Visible:
		return true
	case m.filePicker.Visible:
		return true
	case m.sessionPick.Visible:
		return true
	case m.themePick.Visible:
		return true
	case m.agentPick.Visible:
		return true
	case m.slash.Visible:
		return true
	case m.fleetPicker != nil && m.fleetPicker.Visible:
		return true
	}
	return false
}

// closeAllModals dismisses every modal picker that's currently
// visible. Used by the Ctrl+C top-level binding so a single press
// reliably closes whatever popup is up.
func (m *Model) closeAllModals() {
	if m.modelPicker.Visible {
		m.modelPicker.Close()
	}
	if m.filePicker.Visible {
		m.filePicker.Close()
	}
	if m.sessionPick.Visible {
		m.sessionPick.Close()
	}
	if m.themePick.Visible {
		m.themePick.Close()
	}
	if m.agentPick.Visible {
		m.agentPick.Close()
	}
	if m.slash.Visible {
		m.slash.Close()
		m.slashInline = false
	}
	if m.fleetPicker != nil && m.fleetPicker.Visible {
		m.fleetPicker.Close()
	}
	m.layout()
}

func (m *Model) handleSlash(text string) tea.Cmd {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil
	}
	// Every early-return path below that's appended a system block
	// must reach the viewport. Using defer here instead of duplicating
	// renderBlocks() across every branch avoids the recurring bug
	// where /plugin and /skill silently swallowed their output.
	defer m.renderBlocks()

	// /plugin and /plugin:<name>-<ver> [<tool> [json-args]] — routed
	// before the switch since the plugin-name suffix is dynamic.
	if parts[0] == "/plugin" || strings.HasPrefix(parts[0], "/plugin:") {
		return m.handlePluginSlash(parts)
	}
	if parts[0] == "/skill" || strings.HasPrefix(parts[0], "/skill:") {
		return m.handleSkillSlash(parts)
	}
	switch parts[0] {
	case "/clear":
		if m.state == stateStreaming && m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
			m.state = stateIdle
		}
		if m.state == stateCompactionPending || m.state == stateCompactionEditing || m.compacting {
			m.pendingCompactionSummary = ""
			m.savedDraftBeforeEdit = ""
			m.compactionBlockIdx = 0
			m.compacting = false
			m.state = stateIdle
		}
		m.blocks = nil
		m.msgs = nil
		m.queuedPrompt = ""
		m.turnText = ""
		m.turnThinking = ""
		m.turnToolCalls = nil
		m.renderBlocks()
	case "/help":
		m.showHelp = true
	case "/btw":
		if m.mode == modeBTW {
			m.mode = modeDo
		} else {
			m.mode = modeBTW
		}
	case "/exit", "/quit":
		return tea.Quit
	case "/loop":
		// /loop [duration] <prompt>  or  /loop stop — EP-0036.
		rest := ""
		if len(parts) > 1 {
			rest = strings.TrimSpace(strings.Join(parts[1:], " "))
		}
		return m.handleLoopCmd(rest)
	case "/monitor":
		// /monitor <cmd>  or  /monitor stop — EP-0036.
		rest := ""
		if len(parts) > 1 {
			rest = strings.TrimSpace(strings.Join(parts[1:], " "))
		}
		return m.handleMonitorCmd(rest)
	case "/spawn":
		// /spawn <prompt...> — fire a background agent. Uses the
		// active session's provider+model. EP-0034.
		if len(parts) < 2 {
			m.appendBlock(block{kind: "system", body: "spawn: usage `/spawn <prompt>`"})
			return nil
		}
		prompt := strings.TrimSpace(strings.Join(parts[1:], " "))
		if prompt == "" {
			m.appendBlock(block{kind: "system", body: "spawn: prompt is empty"})
			return nil
		}
		return m.spawnFleetAgent(prompt)
	case "/fleet":
		// /fleet — open the fleet modal. Reads runtime.Fleet's
		// current snapshot.
		entries := m.fleet.List()
		m.fleetPicker.Open(entries)
		m.layout()
		return nil
	case "/cancel", "/stop":
		// Cancel the in-flight turn (if any). The stream goroutine
		// observes ctx.Done and unwinds; the buffered events still
		// flush to the transcript so partial output isn't lost. Any
		// queued prompt is preserved — it'll fire when the current
		// turn's cleanup completes, exactly as if the turn had ended
		// normally.
		m.streamMu.Lock()
		hadStream := m.streamCancel != nil
		if hadStream {
			m.streamCancel()
		}
		m.streamMu.Unlock()
		if hadStream {
			m.appendBlock(block{kind: "system", body: "cancel: in-flight turn cancelled (queued prompt, if any, will run next)"})
		} else if m.queuedPrompt != "" {
			m.queuedPrompt = ""
			m.appendBlock(block{kind: "system", body: "cancel: cleared queued prompt"})
		} else {
			m.appendBlock(block{kind: "system", body: "cancel: no in-flight turn or queued prompt"})
		}
	case "/queue-now", "/force":
		// Force the queued prompt to fire NOW: cancel the current
		// turn (its cleanup will drain the queue and start the
		// queued prompt). Useful when the user typed something
		// after the current turn was already mid-tool-call and
		// wants to skip the rest of the current turn to get to
		// their new request.
		if m.queuedPrompt == "" {
			m.appendBlock(block{kind: "system", body: "queue-now: no queued prompt to force"})
		} else {
			m.streamMu.Lock()
			hadStream := m.streamCancel != nil
			if hadStream {
				m.streamCancel()
			}
			m.streamMu.Unlock()
			if hadStream {
				m.appendBlock(block{kind: "system", body: "queue-now: in-flight turn cancelled; queued prompt will run on cleanup"})
			} else {
				m.appendBlock(block{kind: "system", body: "queue-now: no in-flight turn — queued prompt will run on next dispatch"})
			}
		}
	case "/sidebar":
		m.sidebarOpen = !m.sidebarOpen
	case "/debug":
		m.sidebarDebug = !m.sidebarDebug
		if m.sidebarDebug {
			m.appendBlock(block{kind: "system", body: "sidebar diagnostics: on"})
		} else {
			m.appendBlock(block{kind: "system", body: "sidebar diagnostics: off"})
		}
	case "/todo":
		if len(parts) > 1 {
			title := strings.Join(parts[1:], " ")
			m.todos = append(m.todos, todo{Title: title, Status: "open"})
			if err := m.createTaskFromSlash(title); err != nil {
				m.appendBlock(block{kind: "system", body: err.Error()})
			}
		} else if err := m.openTaskPicker(); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
		}
	case "/tasks", "/task":
		if len(parts) > 2 && (parts[1] == "add" || parts[1] == "new") {
			if err := m.createTaskFromSlash(strings.Join(parts[2:], " ")); err != nil {
				m.appendBlock(block{kind: "system", body: err.Error()})
			}
		} else if len(parts) > 1 {
			m.appendBlock(block{kind: "system", body: "usage: /tasks or /tasks add <title>"})
		} else if err := m.openTaskPicker(); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
		}
	case "/approvals":
		m.appendBlock(block{kind: "system", body: "tool-call approvals were removed; plugins can request approval explicitly via the UI approval capability"})
	case "/tools":
		if m.executor == nil {
			m.appendBlock(block{kind: "system", body: "no tools registered (session unavailable)"})
		} else {
			visible := m.visibleTools()
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Visible tools (%s mode):", m.mode.String()))
			for _, t := range visible {
				cls := m.executor.Registry.ClassOf(t.Name()).String()
				sb.WriteString(fmt.Sprintf("\n  %s [%s] — %s", t.Name(), cls, t.Description()))
			}
			m.appendBlock(block{kind: "system", body: sb.String()})
		}
	case "/agents":
		m.openAgentPicker()
	case "/supervisor":
		m.handleSupervisorSlash(parts)
	case "/stats":
		m.appendBlock(block{kind: "system", body: m.renderStats()})
	case "/ps":
		m.appendBlock(block{kind: "system", body: m.renderPS(false)})
	case "/top":
		// /top is /ps live-updating — in the TUI, show /ps with a note
		// since we don't have a dedicated live-update mode yet.
		m.appendBlock(block{kind: "system", body: m.renderPS(true)})
	case "/kill":
		m.handleKillSlash(parts)
	case "/sandbox":
		m.appendBlock(block{kind: "system", body: m.renderSandboxState()})
	case "/config":
		section := ""
		if len(parts) >= 2 {
			section = parts[1]
		}
		m.appendBlock(block{kind: "system", body: m.renderConfigState(section)})
	case "/model":
		if len(parts) < 2 {
			m.openModelPicker()
		} else {
			old := m.model
			m.model = parts[1]
			body := "model: " + old + " → " + m.model
			if err := m.persistDefaultModel(m.providerName, m.model); err != nil {
				body += "\n" + err.Error()
			}
			m.appendBlock(block{kind: "system", body: body})
		}
	case "/theme":
		if len(parts) < 2 {
			m.openThemePicker()
		} else if err := m.applyThemeSelection(parts[1]); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
		}
	case "/provider":
		if len(parts) > 1 {
			m.appendBlock(block{kind: "system", body: m.providerSetupBody(parts[1])})
			break
		}
		name := m.providerDisplayName()
		if m.provider != nil {
			caps := m.provider.Capabilities()
			body := fmt.Sprintf("provider: %s  (cache=%v thinking=%v vision=%v ctx=%d)",
				name, caps.SupportsPromptCache, caps.SupportsThinking, caps.SupportsVision, caps.MaxContextTokens)
			m.appendBlock(block{kind: "system", body: body})
		} else {
			m.appendBlock(block{kind: "system", body: "provider: " + name + "  (not yet initialised)"})
		}
	case "/status":
		m.showStatus = true
	case "/thinking":
		if len(parts) == 1 {
			m.cycleThinkingDisplayMode()
		} else if mode, ok := parseThinkingDisplayMode(parts[1]); ok {
			m.setThinkingDisplayMode(mode)
		} else {
			m.appendBlock(block{kind: "system", body: "usage: /thinking [show|tail|hide]"})
			break
		}
		m.announceThinkingDisplayMode()
	case "/compact":
		return m.startCompaction()
	case "/context":
		m.appendBlock(block{kind: "system", body: m.renderContextStatus()})
	case "/memory":
		m.handleMemorySlash(parts)
	case "/providers":
		m.appendBlock(block{kind: "system", body: m.renderProvidersOverview()})
	case "/switch":
		if err := m.openSessionPicker(); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
		}
	case "/sessions":
		m.appendBlock(block{kind: "system", body: m.renderSessionsOverview()})
	case "/subagents":
		m.appendBlock(block{kind: "system", body: m.renderSubagentsOverview()})
	case "/adopt":
		m.handleSubagentAdoptSlash(parts)
	case "/new":
		if err := m.createAndSwitchSession(); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
		}
	case "/describe":
		m.handleDescribeSlash(parts)
	case "/budget":
		m.handleBudgetSlash(parts)
	case "/retry":
		return m.handleRetrySlash()
	case "/tool":
		m.handleToolSlash(parts)
	case "/session":
		// /session          — show current session (existing behaviour)
		// /session list     — alias for /sessions
		// /session show <id> — show info for a specific session
		// /session attach <id> — read-only attach (EP-0038 adds RW)
		if len(parts) >= 2 {
			switch parts[1] {
			case "list":
				m.appendBlock(block{kind: "system", body: m.renderSessionsOverview()})
			case "show":
				if len(parts) < 3 {
					m.appendBlock(block{kind: "system", body: "/session show <id> — use `stado session show <id>` for full detail"})
				} else {
					m.appendBlock(block{kind: "system", body: fmt.Sprintf("session show %s: use `stado session show %s` in a terminal for full detail", parts[2], parts[2])})
				}
			case "attach":
				m.handleSessionAttach(parts)
		case "detach":
				m.handleSessionDetach()
			default:
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/session %s: unknown verb. Try: list, show, attach", parts[1])})
			}
		} else {
			m.handleSessionSlash()
		}
	case "/split":
		m.splitView = !m.splitView
		if m.splitView {
			m.appendBlock(block{kind: "system", body: "split view: on — activity (tool + system) on top, conversation on bottom. /split again to toggle off."})
		} else {
			m.appendBlock(block{kind: "system", body: "split view: off — single chat pane restored."})
		}
		m.renderBlocks()
	default:
		m.appendBlock(block{kind: "system", body: "unknown command: " + parts[0] + " (try /help)"})
	}
	m.layout()
	return nil
}

// handleSessionSlash prints the current session's id + worktree so
// users can copy-paste into other shells (for `session fork`,
// `session tree`, `session attach` workflows). Zero-state when
// there's no live session — surfaces a hint rather than failing.
func (m *Model) handleSessionSlash() {
	if m.session == nil || m.session.ID == "" {
		m.appendBlock(block{kind: "system", body: "session: (no live session — launch with `stado` inside a repo)"})
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("id:       %s\n", m.session.ID))
	sb.WriteString(fmt.Sprintf("worktree: %s", m.session.WorktreePath))
	if desc := runtime.ReadDescription(m.session.WorktreePath); desc != "" {
		sb.WriteString(fmt.Sprintf("\nlabel:    %s", desc))
	}
	m.appendBlock(block{kind: "system", body: sb.String()})
}

// handleToolSlash dispatches /tool <verb> [args] commands (EP-0037 §I).
// Session-scoped by default — does not write config files.
func (m *Model) handleToolSlash(parts []string) {
	verb := ""
	if len(parts) >= 2 {
		verb = parts[1]
	}
	switch verb {
	case "", "ls":
		glob := ""
		if len(parts) >= 3 {
			glob = parts[2]
		}
		reg := runtime.BuildDefaultRegistry()
		eff := m.effectiveConfig()
		if eff != nil {
			runtime.ApplyToolFilter(reg, eff)
		}
		autoloaded := runtime.AutoloadedTools(reg, eff)
		autoSet := map[string]bool{}
		for _, t := range autoloaded {
			autoSet[t.Name()] = true
		}
		var lines []string
		for _, t := range reg.All() {
			if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) {
				continue
			}
			state := "enabled"
			if autoSet[t.Name()] {
				state = "autoloaded"
			}
			lines = append(lines, fmt.Sprintf("%-32s %s", t.Name(), state))
		}
		m.appendBlock(block{kind: "system", body: strings.Join(lines, "\n")})

	case "info":
		if len(parts) < 3 {
			m.appendBlock(block{kind: "system", body: "/tool info <name>"})
			return
		}
		reg := runtime.BuildDefaultRegistry()
		t, ok := reg.Get(parts[2])
		if !ok {
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("tool %q not found", parts[2])})
			return
		}
		schema, _ := json.MarshalIndent(t.Schema(), "", "  ")
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("%s\n\n%s\n\nSchema:\n%s", t.Name(), t.Description(), schema)})

	case "cats":
		q := ""
		if len(parts) >= 3 {
			q = strings.ToLower(parts[2])
		}
		var cats []string
		for _, c := range plugins.CanonicalCategories {
			if q == "" || strings.Contains(strings.ToLower(c), q) {
				cats = append(cats, c)
			}
		}
		m.appendBlock(block{kind: "system", body: strings.Join(cats, "\n")})

	case "reload":
		// Runtime-only: wasm instance drop on next call. No config change.
		m.appendBlock(block{kind: "system", body: "/tool reload: wasm instance(s) will be re-initialised on next call."})

	case "enable":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "usage: /tool enable <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable: %v", err)})
				return
			}
			_ = config.WriteToolsListRemove(path, "disabled", args)
			if err := config.WriteToolsListAdd(path, "enabled", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable --save: wrote %s ([tools].enabled += %v)", path, args)})
			return
		}
		m.sessionToolOverrides.enableAdd = appendUnique(m.sessionToolOverrides.enableAdd, args...)
		m.sessionToolOverrides.disableRemove = appendUnique(m.sessionToolOverrides.disableRemove, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable: enabled %v for this session (use --save to persist to .stado/config.toml)", args)})

	case "disable":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "usage: /tool disable <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable: %v", err)})
				return
			}
			// Pull from enabled and autoload before adding to disabled —
			// otherwise the disable is silently masked by either of those
			// lists. Mirrors cmd/stado/tool.go:289-294.
			_ = config.WriteToolsListRemove(path, "enabled", args)
			_ = config.WriteToolsListRemove(path, "autoload", args)
			if err := config.WriteToolsListAdd(path, "disabled", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable --save: wrote %s ([tools].disabled += %v)", path, args)})
			return
		}
		m.sessionToolOverrides.disableAdd = appendUnique(m.sessionToolOverrides.disableAdd, args...)
		m.sessionToolOverrides.enableRemove = appendUnique(m.sessionToolOverrides.enableRemove, args...)
		m.sessionToolOverrides.autoloadRemove = appendUnique(m.sessionToolOverrides.autoloadRemove, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable: disabled %v for this session (use --save to persist)", args)})

	case "autoload":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "usage: /tool autoload <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload: %v", err)})
				return
			}
			if err := config.WriteToolsListAdd(path, "autoload", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload --save: wrote %s ([tools].autoload += %v)", path, args)})
			return
		}
		m.sessionToolOverrides.autoloadAdd = appendUnique(m.sessionToolOverrides.autoloadAdd, args...)
		m.sessionToolOverrides.autoloadRemove = removeFromSlice(m.sessionToolOverrides.autoloadRemove, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload: %v for this session (use --save to persist)", args)})

	case "unautoload":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "usage: /tool unautoload <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload: %v", err)})
				return
			}
			if err := config.WriteToolsListRemove(path, "autoload", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload --save: wrote %s ([tools].autoload -= %v)", path, args)})
			return
		}
		m.sessionToolOverrides.autoloadRemove = appendUnique(m.sessionToolOverrides.autoloadRemove, args...)
		m.sessionToolOverrides.autoloadAdd = removeFromSlice(m.sessionToolOverrides.autoloadAdd, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload: removed %v from this session's autoload (use --save to persist)", args)})

	default:
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool %s: unknown verb. Try: ls, info, cats, reload", verb)})
	}
}

// handleSessionAttach wires /session attach <id> [--pause-parent]. EP-0038 §F.
func (m *Model) handleSessionAttach(parts []string) {
	// parts[0]="/session", parts[1]="attach", parts[2]=id, parts[3..]=flags
	var id string
	pauseParent := false
	for i := 2; i < len(parts); i++ {
		switch parts[i] {
		case "--pause-parent":
			pauseParent = true
		default:
			if id == "" {
				id = strings.TrimPrefix(parts[i], "agent:")
			}
		}
	}
	if id == "" {
		m.appendBlock(block{kind: "system", body: "usage: /session attach <agent-id> [--pause-parent]\nUse /ps to list running agents."})
		return
	}
	if m.fleet == nil {
		m.appendBlock(block{kind: "system", body: "attach: no fleet registry (not in an agent session)"})
		return
	}
	entry, ok := m.fleet.Get(id)
	if !ok {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("attach: agent %q not found — use /ps to list running agents", id)})
		return
	}
	m.attach = attachState{agentID: id, pauseParent: pauseParent}
	status := fmt.Sprintf("attached to agent:%s (session:%s) — your input will be injected into that session. /session detach to return.",
		id[:min8(id)], entry.SessionID[:min8(entry.SessionID)])
	if pauseParent {
		status += " [--pause-parent: parent agent polling paused]"
	}
	m.appendBlock(block{kind: "system", body: status})
}

// handleSessionDetach clears attach mode. EP-0038 §F.
func (m *Model) handleSessionDetach() {
	if m.attach.agentID == "" {
		m.appendBlock(block{kind: "system", body: "not currently attached to any session"})
		return
	}
	prev := m.attach.agentID
	m.attach = attachState{}
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("detached from agent:%s — back to main session", prev[:min8(prev)])})
}

// parseToolMutateArgs splits /tool {enable,disable,autoload,
// unautoload} args into the actual tool names/globs and the --save
// flag.
func parseToolMutateArgs(rest []string) (args []string, save bool) {
	for _, a := range rest {
		if a == "--save" {
			save = true
			continue
		}
		args = append(args, a)
	}
	return
}

// appendUnique returns slice ∪ {extras}, preserving order.
func appendUnique(slice []string, extras ...string) []string {
	seen := map[string]bool{}
	for _, s := range slice {
		seen[s] = true
	}
	for _, e := range extras {
		if seen[e] {
			continue
		}
		seen[e] = true
		slice = append(slice, e)
	}
	return slice
}

// removeFromSlice returns slice with all entries equal to any of
// the targets removed, preserving order.
func removeFromSlice(slice []string, targets ...string) []string {
	skip := map[string]bool{}
	for _, t := range targets {
		skip[t] = true
	}
	out := slice[:0]
	for _, s := range slice {
		if !skip[s] {
			out = append(out, s)
		}
	}
	return out
}

// projectConfigPath returns the path of the project's
// .stado/config.toml. Mirrors cmd/stado/tool.go's
// toolMutateConfigPath default branch (the slash version doesn't
// expose --global; session-scoped override is the equivalent).
func projectConfigPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return cwd + "/.stado/config.toml", nil
}

func (m *Model) handleMemorySlash(parts []string) {
	action := "status"
	if len(parts) > 1 {
		action = strings.ToLower(parts[1])
	}
	workdir := m.sessionActionCWD()
	switch action {
	case "off", "disable", "disabled":
		if err := memory.SetSessionDisabled(workdir, true); err != nil {
			m.appendBlock(block{kind: "system", body: "/memory: " + err.Error()})
			return
		}
		m.appendBlock(block{kind: "system", body: "memory retrieval: disabled for this session"})
	case "on", "enable", "enabled":
		if err := memory.SetSessionDisabled(workdir, false); err != nil {
			m.appendBlock(block{kind: "system", body: "/memory: " + err.Error()})
			return
		}
		m.appendBlock(block{kind: "system", body: "memory retrieval: allowed for this session (requires [memory].enabled = true)"})
	case "status":
		if memory.SessionDisabled(workdir) {
			m.appendBlock(block{kind: "system", body: "memory retrieval: disabled for this session"})
		} else {
			m.appendBlock(block{kind: "system", body: "memory retrieval: allowed for this session (requires [memory].enabled = true)"})
		}
	default:
		m.appendBlock(block{kind: "system", body: "usage: /memory [on|off|status]"})
	}
}

// handleRetrySlash re-generates the last assistant turn without the
// user having to retype the prompt. Truncates m.msgs back to the
// most recent user message (dropping the last assistant + tool-role
// messages) and kicks off a fresh stream. Equivalent to "regenerate"
// buttons in ChatGPT/Claude web UIs — high-value when a response
// was off-target or errored.
//
// No-op + warning when:
//   - a stream is already running (avoid racing)
//   - there's no user message to retry from
//   - the last message is already a user message (no prior assistant
//     turn to discard — just press Enter on an empty prompt)
func (m *Model) handleRetrySlash() tea.Cmd {
	if m.state == stateStreaming {
		m.appendBlock(block{kind: "system", body: "/retry: wait for the current turn to finish"})
		return nil
	}
	// Find the last user-role message in m.msgs.
	lastUser := -1
	for i := len(m.msgs) - 1; i >= 0; i-- {
		if m.msgs[i].Role == agent.RoleUser {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		m.appendBlock(block{kind: "system", body: "/retry: nothing to retry — no user messages yet"})
		return nil
	}
	if lastUser == len(m.msgs)-1 {
		m.appendBlock(block{kind: "system", body: "/retry: last message is already a user prompt — press Enter to submit"})
		return nil
	}
	// Drop everything after the last user message. The LLM will
	// regenerate the assistant (+ tool-use) blocks from scratch on
	// the same prompt.
	m.msgs = m.msgs[:lastUser+1]

	// Sync the visible chat: drop blocks added since the last user
	// block so the screen matches m.msgs. Plain-text block kinds
	// that accompany the retried turn are "assistant" / "thinking" /
	// "tool" / "system". Keep user blocks; prune the rest back to
	// the point where the last user block lives.
	lastUserBlock := -1
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "user" {
			lastUserBlock = i
			break
		}
	}
	if lastUserBlock >= 0 {
		m.blocks = m.blocks[:lastUserBlock+1]
	}
	m.appendBlock(block{kind: "system", body: "/retry: regenerating..."})
	m.renderBlocks()
	return m.startStream()
}

// handleBudgetSlash shows the current budget state or acknowledges a
// hard-cap breach so the session can continue. Three forms:
//
//	/budget                → show warn + hard + current + state
//	/budget ack            → set budgetAcked = true (unblocks turns)
//	/budget reset          → clear budgetAcked so the next breach re-blocks
//
// Raising the actual cap numbers is deliberately not exposed as a
// runtime mutation — the cap lives in config.toml so cost controls
// survive a session restart.
func (m *Model) handleBudgetSlash(parts []string) {
	if len(parts) == 1 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("cost so far: $%.4f\n", m.usage.CostUSD))
		if m.budgetWarnUSD > 0 {
			sb.WriteString(fmt.Sprintf("warn cap: $%.2f\n", m.budgetWarnUSD))
		} else {
			sb.WriteString("warn cap: (unset)\n")
		}
		if m.budgetHardUSD > 0 {
			sb.WriteString(fmt.Sprintf("hard cap: $%.2f", m.budgetHardUSD))
			if m.budgetAcked {
				sb.WriteString("  (acknowledged — turns unblocked)")
			}
		} else {
			sb.WriteString("hard cap: (unset)")
		}
		m.appendBlock(block{kind: "system", body: sb.String()})
		return
	}
	switch parts[1] {
	case "ack":
		m.budgetAcked = true
		m.appendBlock(block{kind: "system", body: "budget: acknowledged — turns unblocked for the rest of this session"})
	case "reset":
		m.budgetAcked = false
		m.appendBlock(block{kind: "system", body: "budget: ack cleared — next breach will re-block"})
	default:
		m.appendBlock(block{kind: "system", body: "usage: /budget  |  /budget ack  |  /budget reset"})
	}
}

// handleSkillSlash implements /skill + /skill:<name>.
//
//	/skill                 — list loaded skills with descriptions
//	/skill:<name>          — inject the body as a user message;
//	                         the next turn picks it up as prompt
//
// Invocation doesn't auto-start a turn; the user still presses Enter
// with an empty input (or types follow-up text) to actually fire.
// That keeps intent explicit — a rogue keystroke can't burn tokens.
func (m *Model) handleSkillSlash(parts []string) tea.Cmd {
	if parts[0] == "/skill" {
		if len(m.skills) == 0 {
			m.appendBlock(block{kind: "system",
				body: "no skills loaded — drop `.stado/skills/<name>.md` files in the repo to define some"})
			return nil
		}
		var sb strings.Builder
		sb.WriteString("loaded skills:")
		for _, sk := range m.skills {
			desc := sk.Description
			if desc == "" {
				desc = "(no description)"
			}
			sb.WriteString(fmt.Sprintf("\n  /skill:%s — %s", sk.Name, desc))
		}
		m.appendBlock(block{kind: "system", body: sb.String()})
		return nil
	}
	// /skill:<name>
	name := strings.TrimPrefix(parts[0], "/skill:")
	if err := m.injectSkill(name); err != nil {
		m.appendBlock(block{kind: "system",
			body: err.Error()})
	}
	return nil
}

// handleDescribeSlash sets the live session's description from
// `/describe <text>` or clears it with `/describe --clear`. Without
// args, prints the current description. Mirrors the CLI
// `stado session describe` subcommand so users can label a session
// from inside the TUI without dropping to a shell.
func (m *Model) handleDescribeSlash(parts []string) {
	if m.session == nil {
		m.appendBlock(block{kind: "system", body: "/describe: no live session"})
		return
	}
	wt := m.session.WorktreePath

	// Read-only form.
	if len(parts) == 1 {
		if d := runtime.ReadDescription(wt); d != "" {
			m.appendBlock(block{kind: "system", body: "description: " + d})
		} else {
			m.appendBlock(block{kind: "system", body: "(no description set — /describe <text> to add one)"})
		}
		return
	}

	// --clear form.
	if len(parts) == 2 && parts[1] == "--clear" {
		if err := runtime.WriteDescription(wt, ""); err != nil {
			m.appendBlock(block{kind: "system", body: "/describe: clear failed: " + err.Error()})
			return
		}
		m.appendBlock(block{kind: "system", body: "description cleared"})
		return
	}

	text := strings.TrimSpace(strings.Join(parts[1:], " "))
	if text == "" {
		m.appendBlock(block{kind: "system",
			body: "/describe: empty text — use /describe --clear to remove the label"})
		return
	}
	if err := runtime.WriteDescription(wt, text); err != nil {
		m.appendBlock(block{kind: "system", body: "/describe: write failed: " + err.Error()})
		return
	}
	m.appendBlock(block{kind: "system", body: "description set: " + text})
}

// handlePluginSlash routes `/plugin` and `/plugin:<name>-<ver>` forms:
//
//	/plugin                                      → list installed plugins
//	/plugin:<name>-<ver>                         → list that plugin's tools
//	/plugin:<name>-<ver> <tool>                  → run with args={}
//	/plugin:<name>-<ver> <tool> {"key":"val",…}  → run with supplied JSON
//
// Verifies manifest signature + wasm digest against the trust store on
// every invocation (cheap, and catches a tampered-after-install plugin
// before it runs). Tool execution happens on a tea.Cmd goroutine so
// the UI stays responsive — result arrives as pluginRunResultMsg.
func (m *Model) handlePluginSlash(parts []string) tea.Cmd {
	cfg, err := config.Load()
	if err != nil {
		m.appendBlock(block{kind: "system", body: "plugin: config load: " + err.Error()})
		return nil
	}
	pluginRoots := cfg.AllPluginDirs() // EP-0035: global + project .stado/plugins/

	// Bare /plugin → list installed.
	if parts[0] == "/plugin" {
		m.appendBlock(block{kind: "system", body: renderInstalledPluginList(pluginRoots...)})
		return nil
	}

	nameVer := strings.TrimPrefix(parts[0], "/plugin:")
	if nameVer == "" {
		m.appendBlock(block{
			kind: "system",
			body: "usage: /plugin:<name>-<version> <tool> [json-args]  (see /plugin to list installed)",
		})
		return nil
	}

	pluginDir, err := plugins.InstalledDirInAny(pluginRoots, nameVer)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "plugin: " + err.Error()})
		return nil
	}
	if _, err := os.Stat(pluginDir); err != nil {
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf("plugin %q not installed (run `stado plugin install <dir>` first)", nameVer),
		})
		return nil
	}

	mf, sig, err := plugins.LoadFromDir(pluginDir)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "plugin load: " + err.Error()})
		return nil
	}
	wasmPath := filepath.Join(pluginDir, "plugin.wasm")
	wasmBytes, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "plugin digest: " + err.Error()})
		return nil
	}
	if err := runtime.VerifyInstalledPlugin(context.Background(), cfg, pluginDir, mf, sig); err != nil {
		m.appendBlock(block{kind: "system", body: "plugin verify: " + err.Error()})
		return nil
	}

	// No tool name → describe the plugin and list its tools.
	if len(parts) < 2 {
		m.appendBlock(block{kind: "system", body: renderPluginTools(nameVer, mf)})
		return nil
	}

	toolName := parts[1]
	argsJSON := "{}"
	if len(parts) >= 3 {
		argsJSON = strings.Join(parts[2:], " ")
	}

	var tdef *plugins.ToolDef
	for i := range mf.Tools {
		if mf.Tools[i].Name == toolName {
			tdef = &mf.Tools[i]
			break
		}
	}
	if tdef == nil {
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf("tool %q not declared in plugin %s — try /plugin:%s to list tools",
				toolName, nameVer, nameVer),
		})
		return nil
	}

	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("plugin %s: invoking %s…", nameVer, toolName),
	})
	m.renderBlocks()

	return runPluginToolAsync(m.cfg, pluginDir, mf, *tdef, argsJSON, nameVer, wasmBytes, m.buildPluginBridge(mf.Name), tuiApprovalBridge{model: m})
}

// openModelPicker builds the item list for the current provider +
// any reachable local runners, then opens the modal picker. See
// internal/tui/modelpicker for the picker itself.
// catalogProviders is the static list of provider names that
// modelpicker.CatalogFor knows about. Kept here so openModelPicker
// can fan out across all of them rather than only the currently-
// active provider — without that, picking up a new model from
// anthropic / openai / google etc. requires switching provider
// FIRST, which defeats the picker's purpose.
var catalogProviders = []string{
	"anthropic", "openai", "google", "groq", "deepseek",
	"mistral", "xai", "cerebras",
}

// bundledHostedPresets is the set of builtin hosted-API preset names
// that the picker should attempt to live-fetch /v1/models from when
// the user has the corresponding API-key env var set. Distinct from
// catalogProviders (which carry hardcoded model lists): these
// providers' model rosters change frequently enough that hardcoding
// is wrong, and they don't have a dedicated SDK provider — they're
// reached over the OAI-compat builtin preset path. Source of truth
// for endpoint + apiKeyEnv is config.BuiltinInferencePreset.
var bundledHostedPresets = []string{
	"ollama-cloud",
	"openrouter",
}

// wrapAgentCatalogs maps an integrations agent name → the catalog
// provider whose model list reflects what that agent can run. When
// the picker fans out across detected ACP/MCP wrap-capable agents,
// each entry gets the wrapped agent's models tagged with
// ProviderName = "<agent>-acp" / "-mcp", so picking a specific model
// (e.g. gemini-2.5-flash via gemini-acp) is one-step. Agents without
// a clean static catalog (opencode is a multi-provider router,
// hermes ditto) get a single generic entry instead — set to "".
var wrapAgentCatalogs = map[string]string{
	"gemini": "google",    // gemini-cli wraps Google's gemini-* models
	"claude": "anthropic", // claude-cli wraps Anthropic models
	"codex":  "openai",    // codex CLI wraps OpenAI models
	// opencode/zed/hermes route across multiple providers — fall back
	// to a single generic entry so the user can switch + then pick a
	// model via the agent's own /model command.
}

func (m *Model) openModelPicker() {
	// Helper: only fan out a hosted provider's static catalog when
	// stado has the auth in env. Skips listing claude-opus-* /
	// gpt-5 / gemini-2.5-* etc. when ANTHROPIC_API_KEY /
	// OPENAI_API_KEY / GEMINI_API_KEY (etc.) aren't set — listing
	// models the user can't actually use is misleading.
	hasNativeAuth := func(name string) bool {
		envName := config.ProviderAPIKeyEnv(name)
		if envName == "" {
			return true // unknown / no-auth-required → assume yes
		}
		return os.Getenv(envName) != ""
	}

	// Start with the active provider's catalog so its entries lead
	// the list (the picker preserves order; recents/favorites
	// prepend later). Active-provider catalog is always shown so the
	// user can see their current option even mid-config.
	items := modelpicker.CatalogFor(m.providerName)
	seenIDs := map[string]bool{}
	for _, it := range items {
		seenIDs[it.ID] = true
	}

	// Add catalogs for OTHER known hosted providers — so the user
	// can switch provider+model in one selection. Skip the active
	// one (already added above) and skip ids we've already seen.
	// Skip whole catalogs when the provider's API key env var is
	// unset.
	for _, name := range catalogProviders {
		if name == m.providerName {
			continue
		}
		if !hasNativeAuth(name) {
			continue
		}
		for _, it := range modelpicker.CatalogFor(name) {
			if seenIDs[it.ID] {
				continue
			}
			seenIDs[it.ID] = true
			items = append(items, it)
		}
	}

	// Overlay detected local runners — if the active provider matches
	// a probed runner's name, the catalog entries get retagged
	// "<name> · detected"; new ids append. Otherwise local runners
	// show up alongside so the user can pick a different backend
	// right from the picker.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	for _, r := range localdetect.DetectBundled(ctx) {
		models := r.RunnableModels()
		if r.Reachable && r.Name == m.providerName {
			items = modelpicker.MergeLocal(items, r.Name, true, models)
			continue
		}
		if r.Reachable {
			for _, modelID := range models {
				items = append(items, modelpicker.Item{
					ID:           modelID,
					Origin:       r.Name + " · detected",
					ProviderName: r.Name,
				})
			}
		}
	}

	// Live-fetch /v1/models for hosted OAI-compat presets — both
	// user-configured ones (`[inference.presets.<name>]`) and
	// BUILTIN ones whose API key is in the environment (ollama-cloud,
	// openrouter, etc.). localdetect only probes localhost endpoints;
	// without this branch, hosted presets show no models in the
	// picker even when the user has authenticated them.
	if m.cfg != nil {
		seenPreset := map[string]bool{}
		// User-configured presets first (override builtin endpoints
		// with the same name).
		for name, preset := range m.cfg.Inference.Presets {
			if preset.Endpoint == "" {
				continue
			}
			seenPreset[name] = true
			if isLocalEndpoint(preset.Endpoint) {
				continue // localdetect already handled localhost ones
			}
			ids := fetchPresetModelIDs(ctx, preset.Endpoint, config.ResolvePresetAPIKey(name, preset))
			for _, id := range ids {
				items = append(items, modelpicker.Item{
					ID: id, Origin: name + " · live", ProviderName: name,
				})
			}
		}
		// Builtin hosted presets (ollama-cloud, openrouter, litellm
		// when at non-localhost). Use BuiltinInferencePreset to
		// resolve endpoint + apiKeyEnv. Only fetch when the env var
		// is actually set — no point hitting the API without auth
		// since /v1/chat/completions will 401 once the user picks a
		// model anyway.
		for _, name := range bundledHostedPresets {
			if seenPreset[name] {
				continue
			}
			endpoint, apiKeyEnv, ok := config.BuiltinInferencePreset(name)
			if !ok || endpoint == "" || isLocalEndpoint(endpoint) {
				continue
			}
			apiKey := ""
			if apiKeyEnv != "" {
				apiKey = os.Getenv(apiKeyEnv)
			}
			if apiKey == "" {
				continue // no auth → skip silently; UI shows other providers
			}
			ids := fetchPresetModelIDs(ctx, endpoint, apiKey)
			for _, id := range ids {
				items = append(items, modelpicker.Item{
					ID: id, Origin: name + " · live", ProviderName: name,
				})
			}
		}
	}

	// ACP/MCP wrap providers — fan out the wrapped agent's underlying
	// model catalog tagged with the wrap provider name, so picking
	// e.g. gemini-2.5-flash via gemini-acp is one selection. Agents
	// without a clean static catalog (opencode/zed/hermes are
	// multi-provider routers) get a single generic entry instead.
	for _, d := range integrations.DetectInstalled(ctx) {
		if d.BinaryPath == "" {
			continue
		}
		catalogName, hasCatalog := wrapAgentCatalogs[d.Name]
		var underlying []modelpicker.Item
		if hasCatalog && catalogName != "" {
			underlying = modelpicker.CatalogFor(catalogName)
		}
		if len(d.ACPArgs) > 0 {
			wrapName := d.Name + "-acp"
			if len(underlying) > 0 {
				for _, it := range underlying {
					it.ProviderName = wrapName
					it.Origin = d.Name + "-acp · " + it.Origin
					items = append(items, it)
				}
			} else {
				items = append(items, modelpicker.Item{
					ID: d.Name, ProviderName: wrapName,
					Origin: d.Name + " · ACP wrap (agent picks model)",
				})
			}
		}
		if d.MCPWrapTools[0] != "" {
			wrapName := d.Name + "-mcp"
			if len(underlying) > 0 {
				for _, it := range underlying {
					it.ProviderName = wrapName
					it.Origin = d.Name + "-mcp · " + it.Origin
					items = append(items, it)
				}
			} else {
				items = append(items, modelpicker.Item{
					ID: d.Name, ProviderName: wrapName,
					Origin: d.Name + " · MCP wrap (agent picks model)",
				})
			}
		}
	}

	items = prependModelRecents(items, m.modelRecents())
	items = prependModelFavorites(items, m.modelFavorites())

	if len(items) == 0 {
		m.appendBlock(block{kind: "system",
			body: "model picker: no known models for provider " + m.providerName +
				".\nUse `/model <exact-id>` to set one by name."})
		return
	}
	m.modelPicker.Open(items, m.model)
	m.layout()
}

// fetchPresetModelIDs calls <endpoint>/models with optional Bearer
// auth and returns the ids OAI-compat servers list under .data[].id.
// Best-effort: any failure (timeout, non-200, malformed JSON) yields
// an empty slice. The picker degrades gracefully — the preset just
// shows no models, same as before this fetch existed.
//
// Endpoint may include the trailing /v1; we don't append it. Auth
// header is omitted entirely when apiKey is empty (some servers
// like ollama.com expose /v1/models without auth, even though chat
// completions require it).
func fetchPresetModelIDs(ctx context.Context, endpoint, apiKey string) []string {
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/models", nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	cli := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := cli.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.ID != "" {
			out = append(out, d.ID)
		}
	}
	return out
}

// renderSessionsOverview is the backing formatter for the `/sessions`
// slash command. Enumerates every other session for the current repo
// with last-active time, turn/message/compaction counts, and a
// `ctrl+x l` hint per row for live switching.
func (m *Model) renderSessionsOverview() string {
	if m.session == nil || m.session.Sidecar == nil {
		return "/sessions: no live session — run `stado session list` instead."
	}
	worktreeRoot := filepath.Dir(m.session.WorktreePath)
	sc := m.session.Sidecar

	ids, err := listSessionIDs(worktreeRoot, sc)
	if err != nil {
		return "/sessions: could not list session refs: " + err.Error()
	}
	// Skip our own session — it's the one the user is already in.
	delete(ids, m.session.ID)

	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	var b strings.Builder
	fmt.Fprintf(&b, "Current session: %s  (turns %d · msgs %d)\n",
		m.session.ID, m.session.Turn(), len(m.msgs))
	b.WriteString("Policy: inactive sessions are parked; switch only when no turn, queued prompt, tool, compaction, or background plugin tick is active.\n")
	if len(sorted) == 0 {
		b.WriteString("\nNo other sessions for this repo.")
		return b.String()
	}
	// Filter out sessions with no completed turns — aborted runs
	// and half-typed orphan prompts leave worktrees behind and used
	// to flood /sessions with stale UUIDs (one msg persisted but
	// never a real turn). Turns == 0 means no work boundary was ever
	// committed, so the session can't be meaningfully resumed.
	rows := make([]runtime.SessionSummary, 0, len(sorted))
	hidden := 0
	for _, id := range sorted {
		r := runtime.SummariseSession(worktreeRoot, sc, id)
		if r.Turns == 0 && r.Compactions == 0 {
			hidden++
			continue
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		if hidden > 0 {
			fmt.Fprintf(&b, "\nNo other active sessions (%d empty session(s) hidden).", hidden)
		} else {
			b.WriteString("\nNo other sessions for this repo.")
		}
		return b.String()
	}
	b.WriteString("\nOther sessions:\n")
	for _, r := range rows {
		label := r.ID
		if r.Description != "" {
			label = fmt.Sprintf("%s  \"%s\"", r.ID, r.Description)
		}
		fmt.Fprintf(&b, "  %s\n", label)
		fmt.Fprintf(&b, "    %s  turns=%d msgs=%d compact=%d  %s\n",
			r.LastActiveFormatted(), r.Turns, r.Msgs, r.Compactions, r.Status)
		fmt.Fprintf(&b, "    switch:  ctrl+x l  (or stado session resume %s)\n", r.ID)
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "\n(%d empty session(s) hidden — run `stado session gc --apply` to clean up)", hidden)
	}
	return b.String()
}

// renderProvidersOverview is the backing formatter for the `/providers`
// slash command. Lists the currently active provider plus every
// reachable local runner, each with its model count + a representative
// model name. Re-probes on each invocation so the list reflects
// right-now state (a user might have started LM Studio mid-session).
func (m *Model) renderProvidersOverview() string {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return m.renderProvidersOverviewFromResults(localdetect.DetectBundled(ctx))
}

func (m *Model) renderProvidersOverviewFromResults(results []localdetect.Result) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("active provider: %s  (model: %s)\n",
		m.providerDisplayName(), m.model))
	credential, _, action := providerCredentialHealth(m.providerDisplayName())
	fmt.Fprintf(&b, "credentials: %s  (%s)\n", credential, action)

	b.WriteString("\nlocal runners on this machine:\n")
	any := false
	for _, r := range results {
		models := r.RunnableModels()
		switch {
		case !r.Reachable:
			fmt.Fprintf(&b, "  %-9s %s  — not running\n", r.Name, r.Endpoint)
		case r.LoadStateKnown && len(models) == 0:
			any = true
			fmt.Fprintf(&b, "  %-9s %s  — running · %d installed model(s), none loaded\n", r.Name, r.Endpoint, len(r.Models))
			if hint := localRunnerNoModelsHint(r.Name); hint != "" {
				fmt.Fprintf(&b, "    next: %s\n", hint)
			}
		case len(models) == 0:
			any = true
			fmt.Fprintf(&b, "  %-9s %s  — running · no models loaded\n", r.Name, r.Endpoint)
			if hint := localRunnerNoModelsHint(r.Name); hint != "" {
				fmt.Fprintf(&b, "    next: %s\n", hint)
			}
		default:
			any = true
			fmt.Fprintf(&b, "  %-9s %s  — running · %d model(s), e.g. %s\n",
				r.Name, r.Endpoint, len(models), models[0])
		}
	}
	if any {
		b.WriteString("\nSwitch with `/model <name>` (current session) or\n")
		b.WriteString("`STADO_DEFAULTS_PROVIDER=<name>` on the next launch.")
	}
	return b.String()
}

func localRunnerNoModelsHint(provider string) string {
	switch strings.TrimSpace(provider) {
	case "ollama":
		return "pull one with `ollama pull <model>`, then reopen `/model`"
	case "lmstudio":
		return "load a model in the LM Studio developer page or run `lms load <model>`"
	case "llamacpp":
		return "restart llama.cpp with `llama-server -m <model>`"
	case "vllm":
		return "restart vLLM with `vllm serve <model>`"
	default:
		return ""
	}
}

// ── EP-0033 supervisor lane slash commands ────────────────────────────────

func (m *Model) handleSupervisorSlash(parts []string) {
	verb := ""
	if len(parts) >= 2 {
		verb = parts[1]
	}
	switch verb {
	case "on", "enable":
		if m.cfg != nil {
			m.cfg.Supervisor.Enabled = true
		}
		m.appendBlock(block{kind: "system", body: "supervisor: enabled — questions and short inputs will be answered immediately while the worker continues"})
	case "off", "disable":
		if m.cfg != nil {
			m.cfg.Supervisor.Enabled = false
		}
		m.appendBlock(block{kind: "system", body: "supervisor: disabled — all input queues for the worker"})
	case "status", "":
		enabled := m.cfg != nil && m.cfg.Supervisor.Enabled
		if enabled {
			model := ""
			if m.cfg.Supervisor.Model != "" {
				model = " (model: " + m.cfg.Supervisor.Model + ")"
			}
			m.appendBlock(block{kind: "system", body: "supervisor: enabled" + model + "\nClassifier: question-heuristic (? suffix = answer, action verb = queue)"})
		} else {
			m.appendBlock(block{kind: "system", body: "supervisor: disabled — /supervisor on to enable"})
		}
	default:
		m.appendBlock(block{kind: "system", body: "/supervisor on|off|status"})
	}
}

// ── EP-0038 §H runtime introspection slash commands ───────────────────────

// renderStats formats /stats output: tokens, cost, agents, uptime. EP-0038 §H.
func (m *Model) renderStats() string {
	var sb strings.Builder
	sb.WriteString("session stats\n")
	sb.WriteString(fmt.Sprintf("  input tokens:  %s\n", formatTokenCount(m.usage.InputTokens)))
	sb.WriteString(fmt.Sprintf("  output tokens: %s\n", formatTokenCount(m.usage.OutputTokens)))
	sb.WriteString(fmt.Sprintf("  total tokens:  %s\n", formatTokenCount(m.totalTokens())))
	if m.usage.CostUSD > 0 {
		sb.WriteString(fmt.Sprintf("  cost:          $%.4f\n", m.usage.CostUSD))
	}
	if m.fleet != nil {
		entries := m.fleet.List()
		running := 0
		for _, e := range entries {
			if e.Status == runtime.FleetStatusRunning {
				running++
			}
		}
		sb.WriteString(fmt.Sprintf("  agents:        %d total, %d running\n", len(entries), running))
	}
	if !m.turnStart.IsZero() {
		sb.WriteString(fmt.Sprintf("  current turn:  %s\n", time.Since(m.turnStart).Round(time.Second)))
	}
	if m.session != nil && m.session.ID != "" {
		sb.WriteString(fmt.Sprintf("  session:       %s\n", m.session.ID))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderPS formats /ps output: live fleet agents + handles. EP-0038 §H.
func (m *Model) renderPS(_ bool) string {
	if m.fleet == nil {
		return "ps: no fleet registry (not in an agent session)"
	}
	entries := m.fleet.List()
	if len(entries) == 0 {
		return "ps: no agents running"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-20s %-12s %-20s %s\n", "ID", "STATUS", "MODEL", "STARTED"))
	for _, e := range entries {
		age := time.Since(e.StartedAt).Round(time.Second).String()
		agentID := pluginRuntime.FormatFreeStandingHandleID(pluginRuntime.HandleTypeAgent, e.FleetID)
		sb.WriteString(fmt.Sprintf("%-20s %-12s %-20s %s ago\n",
			agentID, string(e.Status), e.Model, age))
		if e.SessionID != "" {
			sessionID := pluginRuntime.FormatFreeStandingHandleID(pluginRuntime.HandleTypeSession, e.SessionID)
			sb.WriteString(fmt.Sprintf("  %-18s driver\n", sessionID))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func min8(s string) int {
	if len(s) < 8 {
		return len(s)
	}
	return 8
}

// handleKillSlash handles /kill <id>. EP-0038 §H.
// Accepts:
//   - typed-prefix form: "agent:bf3e" (preferred, matches /ps output)
//   - bare ID:           "bf3e..."   (back-compat — copy-paste-friendly)
func (m *Model) handleKillSlash(parts []string) {
	if len(parts) < 2 {
		m.appendBlock(block{kind: "system", body: "/kill <agent-id>  — cancel a running agent"})
		return
	}
	raw := parts[1]
	id := raw
	if typ, parsedID, _, err := pluginRuntime.ParseHandleID(raw); err == nil {
		// Typed-prefix form parsed cleanly. Only "agent:" is
		// kill-routable today; proc:/term: don't have cancel paths
		// hooked into the TUI yet.
		if typ != pluginRuntime.HandleTypeAgent {
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("kill: /kill only supports agent handles (got %s)", typ)})
			return
		}
		id = parsedID
	}
	if m.fleet == nil {
		m.appendBlock(block{kind: "system", body: "kill: no fleet registry"})
		return
	}
	if err := m.fleet.Cancel(id); err != nil {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("kill %s: %v", id, err)})
		return
	}
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("kill: cancelled agent %s", id)})
}

// renderSandboxState formats /sandbox output. EP-0038 §H.
func (m *Model) renderSandboxState() string {
	if m.cfg == nil {
		return "sandbox: no config loaded"
	}
	mode := m.cfg.Sandbox.Mode
	if mode == "" {
		mode = "off"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("mode:        %s\n", mode))
	if m.cfg.Sandbox.HTTPProxy != "" {
		sb.WriteString(fmt.Sprintf("http_proxy:  %s\n", m.cfg.Sandbox.HTTPProxy))
	}
	if len(m.cfg.Sandbox.DNSServers) > 0 {
		sb.WriteString(fmt.Sprintf("dns_servers: %v\n", m.cfg.Sandbox.DNSServers))
	}
	if mode == "wrap" {
		runner := m.cfg.Sandbox.Wrap.Runner
		if runner == "" {
			runner = "auto"
		}
		sb.WriteString(fmt.Sprintf("runner:      %s\n", runner))
		if net := m.cfg.Sandbox.Wrap.Network; net != "" {
			sb.WriteString(fmt.Sprintf("network:     %s\n", net))
		}
		if len(m.cfg.Sandbox.Wrap.BindRW) > 0 {
			sb.WriteString(fmt.Sprintf("bind_rw:     %v\n", m.cfg.Sandbox.Wrap.BindRW))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderConfigState formats /config [section] output. EP-0038 §H.
func (m *Model) renderConfigState(section string) string {
	if m.cfg == nil {
		return "config: no config loaded"
	}
	var sb strings.Builder
	show := func(name, val string) {
		if section == "" || strings.HasPrefix(name, section) {
			sb.WriteString(fmt.Sprintf("%-30s %s\n", name, val))
		}
	}
	show("defaults.model", m.cfg.Defaults.Model)
	show("defaults.provider", m.cfg.Defaults.Provider)
	show("sandbox.mode", func() string {
		if m.cfg.Sandbox.Mode == "" {
			return "off"
		}
		return m.cfg.Sandbox.Mode
	}())
	show("tools.autoload", fmt.Sprintf("%v", m.cfg.Tools.Autoload))
	show("tools.disabled", fmt.Sprintf("%v", m.cfg.Tools.Disabled))
	if sb.Len() == 0 {
		return fmt.Sprintf("config: no section matching %q", section)
	}
	return strings.TrimRight(sb.String(), "\n")
}
