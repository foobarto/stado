package tui

// Input event handlers — keystrokes (the largest message family in
// the TUI) and mouse events. KeyMsg dispatches in this order:
//
//   1. Ctrl+C while a modal is open → close all modals.
//   2. Modal-state short-circuits (showStatus, showHelp, approval,
//      choice, compactionPending/Editing, quitConfirm).
//   3. Picker-active dispatch via onPickerKey.
//   4. Prefix-chord (Ctrl+X chords).
//   5. Flat keybinding switch (AppExit, SidebarToggle, mode toggle,
//      tool focus, input submit, etc.).
//
// onKey returns handled=false ONLY for the InputClear fall-through
// (so the editor's own InputClear handler can reset the textarea)
// and the implicit fall-through when no flat keybinding matches.

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/keys"
)

func onMouse(m *Model, msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Click-to-expand on tool/assistant blocks. Only consume left-
	// button presses (v2 splits the old MouseMsg into per-action
	// types — a MouseClickMsg is the press); release / drag / motion /
	// wheel events flow through to the viewport for default scroll
	// behaviour. Hold Shift + drag to bypass app mouse capture and use
	// native terminal selection (most modern terminals support this).
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		if m.handleMessagesClick(click.X, click.Y) {
			m.renderBlocks()
			return m, nil
		}
	}
	// Forward to viewport for scroll-wheel handling.
	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	return m, vpCmd
}

func onKey(m *Model, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	// Ctrl+C closes any open modal popup. Esc still works (each
	// modal handles it internally), but adding Ctrl+C as a secondary
	// close key matches readline conventions and lets the user
	// dismiss popups without leaving home-row. Checked before any
	// modal-specific routing so it pre-empts the modal's own
	// keypress handling.
	if msg.String() == "ctrl+c" && m.anyModalOpen() {
		m.closeAllModals()
		return m, nil, true
	}

	if m.showStatus {
		if action, ok := m.keys.TryPrefix(msg); ok {
			if action == keys.StatusView {
				m.showStatus = false
				m.layout()
			}
			return m, nil, true
		}
		if m.keys.Matches(msg, keys.SessionInterrupt) ||
			m.keys.Matches(msg, keys.TipsToggle) ||
			m.keys.Matches(msg, keys.StatusView) {
			m.showStatus = false
			m.layout()
		}
		return m, nil, true
	}

	if m.showHelp {
		// The help body is taller than most terminals; ↑/↓ and friends
		// scroll it. RenderHelp clamps helpScroll on the next frame, so
		// over-shooting here is harmless.
		page := m.height - 6
		if page < 1 {
			page = 1
		}
		switch msg.String() {
		case "up", "k":
			m.helpScroll--
			return m, nil, true
		case "down", "j":
			m.helpScroll++
			return m, nil, true
		case "pgup":
			m.helpScroll -= page
			return m, nil, true
		case "pgdown":
			m.helpScroll += page
			return m, nil, true
		case "home", "g":
			m.helpScroll = 0
			return m, nil, true
		case "end", "G":
			m.helpScroll = 1 << 30 // clamped to the bottom by RenderHelp
			return m, nil, true
		}
		if m.keys.Matches(msg, keys.SessionInterrupt) || m.keys.Matches(msg, keys.TipsToggle) {
			m.showHelp = false
			m.layout()
		}
		return m, nil, true
	}

	if m.approval != nil {
		if cmd, handled := m.handleApprovalKey(msg); handled {
			return m, cmd, true
		}
	}

	if m.choice != nil {
		if cmd, handled := m.handleChoiceKey(msg); handled {
			return m, cmd, true
		}
	}

	// Compaction confirmation: reuse the Approve / Deny keybindings
	// (y / n by default) so the UX matches tool-call approval.
	// EditSummary ('e') switches into an inline editor where the
	// user can revise the draft before accepting.
	if m.state == stateCompactionPending {
		if m.keys.Matches(msg, keys.Approve) {
			m.resolveCompaction(true)
			m.renderBlocks()
			return m, nil, true
		}
		if m.keys.Matches(msg, keys.Deny) {
			m.resolveCompaction(false)
			m.renderBlocks()
			return m, nil, true
		}
		if m.keys.Matches(msg, keys.EditSummary) {
			m.enterSummaryEdit()
			m.renderBlocks()
			return m, nil, true
		}
		// Any other key while pending is ignored — no accidental msgs
		// mutation while the user reads the summary.
		return m, nil, true
	}

	// Summary-editing state: Enter commits, Esc/Deny cancels. All
	// other keys flow to the editor so the user can type freely.
	if m.state == stateCompactionEditing {
		if m.keys.Matches(msg, keys.InputSubmit) {
			m.commitSummaryEdit()
			m.renderBlocks()
			return m, nil, true
		}
		if m.keys.Matches(msg, keys.Deny) {
			m.cancelSummaryEdit()
			m.renderBlocks()
			return m, nil, true
		}
		inputCmd, _ := m.input.Update(msg)
		return m, inputCmd, true
	}

	// Quit confirmation: y/Enter confirms, n/Esc cancels.
	if m.state == stateQuitConfirm {
		if m.keys.Matches(msg, keys.Approve) || msg.String() == "enter" {
			return m, tea.Quit, true
		}
		if m.keys.Matches(msg, keys.Deny) || msg.String() == "esc" {
			m.state = stateIdle
			m.renderBlocks()
			return m, nil, true
		}
		// Any other key is ignored.
		return m, nil, true
	}

	if model, cmd, handled := onPickerKey(m, msg); handled {
		return model, cmd, true
	}

	// Prefix-chord dispatch: ctrl+x <chord>, etc.
	// Placed after all modal checks so chords don't bypass overlays;
	// placed before flat keybindings so they take priority when
	// the prefix state is active.
	if action, ok := m.keys.TryPrefix(msg); ok {
		if action != "" {
			switch action {
			case keys.ModeToggleBtw:
				if m.mode == modeBTW {
					m.mode = modeDo
				} else {
					m.mode = modeBTW
				}
				m.layout()
			case keys.SidebarNarrower:
				m.resizeSidebar(-sidebarResizeStep)
				m.layout()
			case keys.SidebarWider:
				m.resizeSidebar(sidebarResizeStep)
				m.layout()
			case keys.SessionSwitch:
				if err := m.openSessionPicker(); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
			case keys.AgentSwitch:
				m.openAgentPicker()
				m.layout()
			case keys.ModelSwitch:
				m.openModelPicker()
				m.layout()
			case keys.SessionNew:
				if err := m.createAndSwitchSession(); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
			case keys.TaskView:
				if err := m.openTaskPicker(); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
			case keys.ThemeSwitch:
				m.openThemePicker()
				m.layout()
			case keys.StatusView:
				m.showStatus = true
				m.layout()
			case keys.ThinkingToggle:
				m.cycleThinkingDisplayMode()
				m.announceThinkingDisplayMode()
				m.layout()
			case keys.AppExit:
				m.state = stateQuitConfirm
				m.layout()
			}
		}
		return m, nil, true
	}

	switch {
	case m.keys.Matches(msg, keys.AppExit):
		m.state = stateQuitConfirm
		m.layout()
		return m, nil, true

	case m.keys.Matches(msg, keys.SidebarToggle):
		m.sidebarOpen = !m.sidebarOpen
		m.layout()
		return m, nil, true

	case m.keys.Matches(msg, keys.TipsToggle):
		// Gate `?` to empty input so a literal question mark inside a
		// prompt ("what's this?") inserts as text instead of popping
		// the help overlay. Ctrl+P remains reachable with content in
		// the editor; slash suggestions intentionally start from an
		// empty prompt.
		if m.input.Value() == "" {
			m.showHelp = true
			m.helpScroll = 0
			m.layout()
			return m, nil, true
		}

	case m.keys.Matches(msg, keys.CommandList):
		// Ctrl+P opens the command palette modal. The palette owns
		// its own search input — the main textarea is untouched.
		m.slashInline = false
		m.slash.Open()
		m.layout()
		return m, nil, true

	case msg.Text == "/" && m.input.Value() == "":
		m.slashInline = true
		m.slash.Open()
		m.layout()
		return m, nil, true

	case m.keys.Matches(msg, keys.StatusView):
		m.showStatus = true
		m.layout()
		return m, nil, true

	case m.keys.Matches(msg, keys.SessionInterrupt):
		// Esc / Ctrl+G — readline + Emacs canonical cancel.
		// Priority: clear queued prompt > cancel in-flight stream
		// AND in-flight tool. Mirrors the Ctrl+C (InputClear)
		// behaviour for the empty-input case so all three keys
		// converge on a consistent /cancel semantic.
		//
		// Codex #105 caught that this branch only fired streamCancel,
		// missing toolCancel — during the tool-execution phase
		// streamCancel is nil (cleared at streamDoneMsg) but the tool's
		// context is held by toolCancel, so the operator had no kill
		// switch for a runaway bash / network / plugin tool. Fix:
		// fire both via the cancelRunningStream / cancelRunningTool
		// helpers (see cancel.go).
		if m.queuedPrompt != "" {
			m.queuedPrompt = ""
			for i := len(m.blocks) - 1; i >= 0; i-- {
				if m.blocks[i].kind == "user" && m.blocks[i].queued {
					m.blocks = append(m.blocks[:i], m.blocks[i+1:]...)
					break
				}
			}
			m.appendBlock(block{kind: "system", body: "queued prompt cleared"})
			m.renderBlocks()
			return m, nil, true
		}
		if m.steeringMsg != "" {
			m.steeringMsg = ""
			m.appendBlock(block{kind: "system", body: "steering message cleared"})
			m.renderBlocks()
			return m, nil, true
		}
		streamCancelled := m.cancelRunningStream()
		toolCancelled := m.cancelRunningTool()
		pendingDropped := m.clearPendingToolQueue()
		if streamCancelled || toolCancelled || pendingDropped > 0 {
			body := "turn cancelled"
			if toolCancelled {
				body = "turn cancelled (running tool interrupted)"
			}
			if pendingDropped > 0 {
				body = fmt.Sprintf("%s, %d pending tool(s) dropped", body, pendingDropped)
			}
			m.appendBlock(block{kind: "system", body: body})
			m.renderBlocks()
			return m, nil, true
		}

	case m.keys.Matches(msg, keys.QueueMessage):
		// Alt+Enter — queue the typed message for the NEXT turn (#17).
		// (Was ForceQueue=fire-now; that role moved to InterruptTurn.)
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			m.appendBlock(block{kind: "system", body: "queue: type a message first"})
			m.renderBlocks()
			return m, nil, true
		}
		m.input.History.Push(text)
		m.input.Reset()
		return m, m.applyQueue(text), true

	case m.keys.Matches(msg, keys.InterruptTurn):
		// Ctrl+Enter — cancel the current turn and run the typed message
		// now (#16). Empty input fires an already-queued prompt now (the
		// old ForceQueue capability). Handled so the chord never lands as
		// a literal in the textarea.
		text := strings.TrimSpace(m.input.Value())
		if text != "" {
			m.input.History.Push(text)
			m.input.Reset()
		}
		return m, m.applyInterrupt(text), true

	case m.keys.Matches(msg, keys.ToolExpand):
		m.toggleLastToolExpand()
		m.renderBlocks()
		return m, nil, true

	case m.keys.Matches(msg, keys.ToolFocusPrev):
		m.focusPrevExpandable()
		m.renderBlocks()
		return m, nil, true

	case m.keys.Matches(msg, keys.ToolFocusNext):
		m.focusNextExpandable()
		m.renderBlocks()
		return m, nil, true

	case m.keys.Matches(msg, keys.ModeToggle):
		if m.mode == modeDo {
			m.mode = modePlan
		} else {
			m.mode = modeDo
		}
		m.layout()
		return m, nil, true

	case m.keys.Matches(msg, keys.InputClear):
		// Ctrl+C: clear the chat input only. Cancel semantics live on
		// Esc / Ctrl+G (SessionInterrupt) and force-queue on Alt+Enter
		// (ForceQueue) — Ctrl+C does NOT touch the in-flight stream
		// or queued prompt anymore. The exit key is Ctrl+D.
		//
		// The editor's own InputClear case (input/editor.go) resets
		// the textarea on fall-through; returning handled=false here
		// is what triggers that path.

	case m.keys.Matches(msg, keys.InputSubmit):
		return submitInput(m)
	}

	return m, nil, false
}

// submitInput processes Enter on the chat input. Encapsulated as its
// own function — the body is dense (queue / supervisor lane / attach /
// budget / context-threshold gates) and the dispatcher reads cleaner
// without inlining ~100 lines of submission logic.
func submitInput(m *Model) (tea.Model, tea.Cmd, bool) {
	if m.input.Value() == "" {
		return m, nil, true
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil, true
	}
	tuiTrace("input submit", "state", int(m.state), "chars", len(text), "probe_pending", m.providerProbePending)
	// Enter while a turn is still streaming: queue the prompt for
	// after-done instead of silently dropping it (the old behaviour)
	// or abruptly cancelling (bad UX). The user's block is appended
	// to m.blocks IMMEDIATELY so they see their message in the chat
	// (dogfood-bug: silent queue looked like a freeze). Only m.msgs
	// add + startStream wait for drain — the current turn is
	// mid-stream and must not see the new user message in its
	// context window.
	if m.state == stateStreaming {
		// Slash commands bypass the queue — /clear, /compact, /retry
		// etc. are meta-commands users expect to act immediately even
		// mid-stream. Everything else (regular prompts) gets queued
		// for after-drain.
		if strings.HasPrefix(text, "/") {
			m.input.Reset()
			m.slash.Close()
			m.slashInline = false
			return m, m.handleSlash(text), true
		}
		// EP-0033: supervisor lane — classify input and route accordingly.
		if m.cfg != nil && m.cfg.Supervisor.Enabled {
			switch classifyInput(text) {
			case supervisorAnswer:
				// Route to supervisor (btw) lane — answers immediately
				// from transcript context without queuing.
				m.input.History.Push(text)
				m.input.Reset()
				return m, m.startBtw(text), true
			case supervisorInterrupt:
				// Cancel worker and queue the input.
				if m.streamCancel != nil {
					m.streamCancel()
				}
				m.appendBlock(block{kind: "system", body: "supervisor: interrupting worker — input queued for next turn"})
				m.queuedPrompt = text
				m.input.History.Push(text)
				m.input.Reset()
				m.renderBlocks()
				return m, nil, true
			case supervisorSteer:
				// Inject a steering note and queue.
				steer := "[supervisor steer] " + text
				m.appendBlock(block{kind: "btw", body: steer})
				m.queuedPrompt = text
				m.input.History.Push(text)
				m.input.Reset()
				m.renderBlocks()
				return m, nil, true
			default:
				// supervisorQueue: fall through to normal queue behaviour.
			}
		}
		// #16: a plain message sent mid-turn STEERS — injected into the
		// current turn at the next tool boundary — rather than queuing
		// for the next turn. alt+enter (QueueMessage) / ctrl+enter
		// (InterruptTurn) and /queue //interrupt cover the other intents.
		m.input.History.Push(text)
		m.input.Reset()
		return m, m.applySteer(text), true
	}
	if strings.HasPrefix(text, "/") {
		m.input.Reset()
		m.slash.Close()
		m.slashInline = false
		return m, m.handleSlash(text), true
	}
	// EP-0038 §F: /session attach RW — route input to agent inbox.
	if m.attach.agentID != "" {
		agentID := m.attach.agentID
		if m.fleet != nil {
			if _, ok := m.fleet.Get(agentID); ok {
				m.appendBlock(block{kind: "user", body: text, source: "operator"})
				// Build a bridge adapter to inject the message.
				bridge := &runtime.FleetBridgeAdapter{
					Fleet:   m.fleet,
					Spawner: spawnerFunc(m.buildSubagentSpawner()),
					RootCtx: m.rootCtx,
				}
				_ = bridge.AgentSendMessage(m.rootCtx, agentID, text)
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("→ sent to agent:%s", agentID[:min8(agentID)])})
			} else {
				m.attach = attachState{} // agent gone — auto-detach
				m.appendBlock(block{kind: "system", body: "agent no longer running — detached automatically"})
			}
		}
		m.input.History.Push(text)
		m.input.Reset()
		m.renderBlocks()
		return m, nil, true
	}
	// Budget hard-cap gate (same UX pattern as the context
	// hard-threshold). Draft text stays in input; user clears the
	// block with `/budget ack` which sets budgetAcked for the
	// remainder of the session.
	if m.budgetExceeded() {
		body := fmt.Sprintf(
			"cost $%.2f ≥ hard cap $%.2f — blocked. Continue with:\n"+
				"  · /budget ack — acknowledge and continue this session\n"+
				"  · edit [budget].hard_usd in config.toml to raise the cap",
			m.usage.CostUSD, m.budgetHardUSD)
		m.appendBlock(block{kind: "system", body: body})
		m.renderBlocks()
		return m, nil, true
	}
	// Hard-threshold gate (DESIGN §"Token accounting" 11.2.6).
	// Refuse to start a fresh turn once we're at/above the hard
	// bound — forces the user to /compact or fork before adding
	// more context. The draft text stays in the input so the
	// recovery flow doesn't lose it.
	if m.aboveHardThreshold() {
		if m.hasAutoCompactBackgroundPlugin() {
			m.recoveryPrompt = text
			m.recoveryPluginName = "auto-compact"
			m.recoveryPluginActive = true
			body := fmt.Sprintf(
				"context at %.0f%% (hard threshold %.0f%%) — running bundled auto-compact before replaying your prompt in a child session.",
				100*m.contextFraction(), 100*m.ctxHardThreshold)
			m.appendBlock(block{kind: "system", body: body})
			m.renderBlocks()
			return m, m.tickBackgroundPluginsWithEvent(m.contextOverflowEvent(text)), true
		}
		body := fmt.Sprintf(
			"context at %.0f%% (hard threshold %.0f%%) — blocked. Recover with:\n"+
				"  · /compact — user-confirmed in-TUI summarisation\n"+
				"  · stado session fork <id> --at turns/<N> — branch from an earlier turn",
			100*m.contextFraction(), 100*m.ctxHardThreshold)
		// Offer auto-compact specifically when it's installed — the
		// user doesn't have to remember the exact plugin-id string;
		// we've already found one on disk.
		if ac := m.installedAutoCompact(); ac != "" {
			body += fmt.Sprintf("\n  · /plugin:%s compact — automated compact + fork via the auto-compact plugin", ac)
		}
		m.appendBlock(block{kind: "system", body: body})
		m.renderBlocks()
		return m, nil, true
	}
	if m.provider == nil && m.providerProbePending && m.providerName == "" {
		m.queuedPrompt = text
		m.appendBlock(block{kind: "user", body: text, queued: true})
		m.renderBlocks()
		m.input.History.Push(text)
		m.input.Reset()
		tuiTrace("submit queued behind startup provider probe", "chars", len(text))
		return m, nil, true
	}
	m.input.History.Push(text)
	m.input.Reset()
	if m.mode == modeBTW {
		return m, m.startBtw(text), true
	}
	m.appendUser(text)
	m.renderBlocks()
	return m, m.startStream(), true
}
