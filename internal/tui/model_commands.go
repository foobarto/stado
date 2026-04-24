package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tui/modelpicker"
	"github.com/foobarto/stado/pkg/agent"
)

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
	case "/sidebar":
		m.sidebarOpen = !m.sidebarOpen
	case "/todo":
		// Demo: quick way to test sidebar todo rendering.
		if len(parts) > 1 {
			m.todos = append(m.todos, todo{Title: strings.Join(parts[1:], " "), Status: "open"})
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
	case "/model":
		if len(parts) < 2 {
			m.openModelPicker()
		} else {
			old := m.model
			m.model = parts[1]
			m.appendBlock(block{kind: "system", body: "model: " + old + " → " + m.model})
		}
	case "/provider":
		name := m.providerDisplayName()
		if m.provider != nil {
			caps := m.provider.Capabilities()
			body := fmt.Sprintf("provider: %s  (cache=%v thinking=%v vision=%v ctx=%d)",
				name, caps.SupportsPromptCache, caps.SupportsThinking, caps.SupportsVision, caps.MaxContextTokens)
			m.appendBlock(block{kind: "system", body: body})
		} else {
			m.appendBlock(block{kind: "system", body: "provider: " + name + "  (not yet initialised)"})
		}
	case "/compact":
		return m.startCompaction()
	case "/context":
		m.appendBlock(block{kind: "system", body: m.renderContextStatus()})
	case "/providers":
		m.appendBlock(block{kind: "system", body: m.renderProvidersOverview()})
	case "/switch":
		if err := m.openSessionPicker(); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
		}
	case "/sessions":
		m.appendBlock(block{kind: "system", body: m.renderSessionsOverview()})
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
	case "/session":
		m.handleSessionSlash()
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
	var chosen *skills.Skill
	for i := range m.skills {
		if m.skills[i].Name == name {
			chosen = &m.skills[i]
			break
		}
	}
	if chosen == nil {
		m.appendBlock(block{kind: "system",
			body: fmt.Sprintf("skill %q not found — try /skill for the list", name)})
		return nil
	}
	// Inject as a user message. Append to m.msgs so the next
	// StreamTurn sees it; also render a visible user block so the
	// user knows what was sent.
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, chosen.Body))
	m.appendBlock(block{kind: "user", body: chosen.Body})
	m.renderBlocks()
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
	pluginsRoot := filepath.Join(cfg.StateDir(), "plugins")

	// Bare /plugin → list installed.
	if parts[0] == "/plugin" {
		m.appendBlock(block{kind: "system", body: renderInstalledPluginList(pluginsRoot)})
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

	pluginDir := filepath.Join(pluginsRoot, nameVer)
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
	if err := plugins.VerifyWASMDigest(mf.WASMSHA256, wasmPath); err != nil {
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

	return runPluginToolAsync(pluginDir, mf, *tdef, argsJSON, nameVer, m.buildPluginBridge(mf.Name), tuiApprovalBridge{model: m})
}

// openModelPicker builds the item list for the current provider +
// any reachable local runners, then opens the modal picker. See
// internal/tui/modelpicker for the picker itself.
func (m *Model) openModelPicker() {
	items := modelpicker.CatalogFor(m.providerName)

	// Overlay detected local runners — if the active provider matches
	// a probed runner's name, the catalog entries get retagged
	// "<name> · detected"; new ids append. Otherwise local runners
	// show up alongside so the user can pick a different backend
	// right from the picker.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	for _, r := range localdetect.DetectBundled(ctx) {
		if r.Reachable && r.Name == m.providerName {
			items = modelpicker.MergeLocal(items, r.Name, true, r.Models)
			continue
		}
		if r.Reachable {
			for _, modelID := range r.Models {
				items = append(items, modelpicker.Item{
					ID:           modelID,
					Origin:       r.Name + " · detected",
					ProviderName: r.Name,
				})
			}
		}
	}

	if len(items) == 0 {
		m.appendBlock(block{kind: "system",
			body: "model picker: no known models for provider " + m.providerName +
				".\nUse `/model <exact-id>` to set one by name."})
		return
	}
	m.modelPicker.Open(items, m.model)
	m.layout()
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
	var b strings.Builder
	b.WriteString(fmt.Sprintf("active provider: %s  (model: %s)\n",
		m.providerDisplayName(), m.model))

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	results := localdetect.DetectBundled(ctx)

	b.WriteString("\nlocal runners on this machine:\n")
	any := false
	for _, r := range results {
		switch {
		case !r.Reachable:
			fmt.Fprintf(&b, "  %-9s %s  — not running\n", r.Name, r.Endpoint)
		case len(r.Models) == 0:
			any = true
			fmt.Fprintf(&b, "  %-9s %s  — running · no models loaded\n", r.Name, r.Endpoint)
		default:
			any = true
			fmt.Fprintf(&b, "  %-9s %s  — running · %d model(s), e.g. %s\n",
				r.Name, r.Endpoint, len(r.Models), r.Models[0])
		}
	}
	if any {
		b.WriteString("\nSwitch with `/model <name>` (current session) or\n")
		b.WriteString("`STADO_DEFAULTS_PROVIDER=<name>` on the next launch.")
	}
	return b.String()
}
