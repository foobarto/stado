package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/providers/localdetect"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/input"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/modelpicker"
	"github.com/foobarto/stado/internal/tui/overlays"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// block is the UI-level conversation unit. One conversation is a slice of these.
type block struct {
	kind string // "user" | "assistant" | "thinking" | "tool"
	body string

	// Tool-call specific
	toolID    string
	toolName  string
	toolArgs  string
	toolResult string
	startedAt  time.Time
	endedAt    time.Time
	expanded   bool
}

type todo struct {
	Title  string
	Status string // "open" | "in_progress" | "done"
}

// sessionState controls the status bar + input gating.
type sessionState int

const (
	stateIdle sessionState = iota
	stateStreaming
	stateApproval
	// stateCompactionPending: a summarisation stream has finished and
	// the proposed summary is visible in the conversation. The next
	// 'y' / 'n' / 'e' / '/' keystroke resolves it. See DESIGN
	// §"Compaction": no replacement without explicit confirmation.
	stateCompactionPending
	// stateCompactionEditing: user pressed 'e' on a pending summary —
	// the editor holds the summary text for inline revision. Enter
	// commits the edit (back to stateCompactionPending), Esc cancels.
	stateCompactionEditing
	stateError
)

// inputMode switches the agent between read-only analysis ("Plan") and
// full execution ("Do"). Plan mode filters Mutating + Exec tools out of
// the TurnRequest so the model naturally produces an outline/plan.
type inputMode int

const (
	modeDo inputMode = iota
	modePlan
)

func (m inputMode) String() string {
	if m == modePlan {
		return "Plan"
	}
	return "Do"
}

// Internal messages used by the bubbletea update loop.
type (
	streamEventMsg   struct{ ev agent.Event }
	streamErrorMsg   struct{ err error }
	streamDoneMsg    struct{}
	toolsExecutedMsg struct{ results []agent.ToolResultBlock }
	// pluginRunResultMsg carries the outcome of a `/plugin:...` invocation
	// back to the Update loop. Rendered as a system block so the user
	// sees the tool's return value alongside the conversation flow.
	pluginRunResultMsg struct {
		plugin  string
		tool    string
		content string
		errMsg  string
	}
)

// Model is the root bubbletea model for stado's TUI.
type Model struct {
	// Config + infrastructure
	cwd      string
	keys     *keys.Registry
	theme    *theme.Theme
	renderer *render.Renderer

	// Provider is resolved lazily on the first StreamTurn so stado can boot
	// without credentials present. buildProvider runs on demand; errors
	// surface as in-UI messages instead of crashing startup.
	provider      agent.Provider
	buildProvider func() (agent.Provider, error)
	providerName  string // displayed before lazy build resolves the real name
	model         string

	// Tool execution + git state. executor may be nil (no session) in which
	// case tool calls are reported but not executed.
	executor *tools.Executor
	session  *stadogit.Session

	// UI components
	input       *input.Editor
	slash       *palette.Model
	modelPicker *modelpicker.Model
	vp          viewport.Model
	showHelp    bool

	// mode is Do (default — all tools allowed) or Plan (mutating + exec
	// tools hidden from the model so it produces an analysis-only
	// response). Tab toggles.
	mode inputMode

	// Conversation state
	blocks   []block
	msgs     []agent.Message
	todos    []todo

	// Streaming
	state        sessionState
	streamCancel context.CancelFunc
	streamMu     sync.Mutex
	errorMsg     string

	// Aggregate usage across turns. usage.InputTokens is the LAST turn's
	// prompt size (not cumulative) — it's the correct input for the
	// context-window percentage calculation. OutputTokens and CostUSD
	// remain cumulative.
	usage agent.Usage

	// Context thresholds from config.Context. Compared against
	// usage.InputTokens / Capabilities.MaxContextTokens. See DESIGN
	// §"Token accounting".
	ctxSoftThreshold float64
	ctxHardThreshold float64

	// tokenCounterChecked is set once we've probed the provider for
	// agent.TokenCounter. tokenCounterPresent records the result so
	// subsequent turns don't re-probe.
	tokenCounterChecked bool
	tokenCounterPresent bool

	// Compaction state. pendingCompactionSummary holds the proposed
	// summary between the end of the summarisation stream and the user's
	// y/n decision. Consumed by resolveCompaction.
	pendingCompactionSummary string
	// savedDraftBeforeEdit stashes the in-progress user prompt when
	// the user presses 'e' to enter summary-editing mode, so the
	// draft survives the edit round-trip.
	savedDraftBeforeEdit string
	// compactionBlockIdx remembers which m.blocks entry holds the
	// visible compaction draft, so an edit updates the same block the
	// user is looking at instead of appending a new one.
	compactionBlockIdx int
	// compacting marks a summarisation stream in-flight so we can route
	// its text deltas into a "compaction-preview" block rather than the
	// regular assistant block.
	compacting bool

	// Layout
	width          int
	height         int
	sidebarOpen    bool

	// Approval
	approval *approvalRequest

	// Back-channel for events from the provider goroutine.
	program *tea.Program

	// Per-turn accumulators (reset on startStream).
	turnText       string
	turnThinking   string
	turnThinkSig   string
	turnToolCalls  []agent.ToolUseBlock

	// Approval queue: calls waiting for user decision + the results already
	// collected during this tool batch. When the queue drains we emit a
	// toolsExecutedMsg and the agent loop continues.
	pendingCalls   []agent.ToolUseBlock
	pendingResults []agent.ToolResultBlock

	// Session-scoped "always allow this tool" — reset when the TUI exits.
	// PLAN cross-cutting: session-scoped remember with explicit forget.
	rememberedAllow map[string]bool
}

type approvalRequest struct {
	toolName string
	args     string
	toolID   string
}

// NewModel wires the TUI. buildProvider is called on the first streamed turn
// — no credentials are read until then, so stado boots even without an API
// key. providerName labels the status bar before the lazy build resolves.
func NewModel(cwd, modelName, providerName string, buildProvider func() (agent.Provider, error), rnd *render.Renderer, keyReg *keys.Registry) *Model {
	m := &Model{
		cwd:              cwd,
		keys:             keyReg,
		theme:            rnd.Theme(),
		renderer:         rnd,
		buildProvider:    buildProvider,
		providerName:     providerName,
		model:            modelName,
		input:            input.New(keyReg),
		slash:            palette.New(),
		modelPicker:      modelpicker.New(),
		vp:               viewport.New(0, 0),
		sidebarOpen:      true,
		ctxSoftThreshold: 0.70, // DESIGN §"Token accounting" defaults.
		ctxHardThreshold: 0.90,
	}
	return m
}

// SetContextThresholds overrides the soft/hard threshold defaults. Called
// from the TUI entry point to propagate [context] config values. Values
// outside (0, 1] are rejected and the previous value kept.
func (m *Model) SetContextThresholds(soft, hard float64) {
	if soft > 0 && soft <= 1 {
		m.ctxSoftThreshold = soft
	}
	if hard > 0 && hard <= 1 {
		m.ctxHardThreshold = hard
	}
}

// ensureProvider lazy-builds the provider on first use. On failure sets the
// error state and appends an actionable system-role hint to the chat.
func (m *Model) ensureProvider() bool {
	if m.provider != nil {
		return true
	}
	if m.buildProvider == nil {
		m.state = stateError
		m.errorMsg = "no provider configured"
		m.appendBlock(block{kind: "system", body: "No provider configured.\n" +
			"Run `stado config init` to scaffold ~/.config/stado/config.toml,\n" +
			"or set `defaults.provider` there. 'ollama' works locally with no key."})
		return false
	}
	p, err := m.buildProvider()
	if err != nil {
		m.state = stateError
		m.errorMsg = err.Error()
		body := "Provider unavailable: " + err.Error()
		if hint := providerErrorHint(m.providerName, err.Error()); hint != "" {
			body += "\n\n" + hint
		}
		// The local-runner hint does a real network probe (bounded at
		// ~1.5s total). Running it synchronously here froze the UI
		// before the error surfaced. Fire it as an async tea.Cmd so the
		// error appears instantly + the hint arrives when ready.
		m.appendBlock(block{kind: "system", body: body})
		go func() {
			if h := detectRunningLocalHint(); h != "" {
				m.sendMsg(localHintMsg{body: h})
			}
		}()
		return false
	}
	m.provider = p
	return true
}

// localHintMsg carries an async-produced local-runner hint back to the
// main bubbletea goroutine. Dispatched by the goroutine in ensureProvider
// and consumed in Update → appendBlock.
type localHintMsg struct{ body string }

// detectRunningLocalHint probes the bundled local endpoints and returns a
// human message when any responded. Stays under ~1.5s total thanks to
// localdetect's per-probe timeout + concurrency. Empty return = no
// running local runner detected (or the probe was interrupted).
func detectRunningLocalHint() string {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return renderLocalRunnerHint(localdetect.DetectBundled(ctx))
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

// renderLocalRunnerHint is the pure formatter behind detectRunningLocalHint.
// Kept as a standalone function so tests can exercise the output without
// needing real endpoints.
func renderLocalRunnerHint(results []localdetect.Result) string {
	var running []localdetect.Result
	for _, r := range results {
		if r.Reachable {
			running = append(running, r)
		}
	}
	if len(running) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Detected running local provider(s) on this machine:\n")
	for _, r := range running {
		switch {
		case len(r.Models) == 0:
			fmt.Fprintf(&b, "  %-9s %s  (no models loaded)\n", r.Name, r.Endpoint)
		case len(r.Models) == 1:
			fmt.Fprintf(&b, "  %-9s %s  (1 model: %s)\n", r.Name, r.Endpoint, r.Models[0])
		default:
			fmt.Fprintf(&b, "  %-9s %s  (%d models, e.g. %s)\n",
				r.Name, r.Endpoint, len(r.Models), r.Models[0])
		}
	}
	fmt.Fprintf(&b, "\nSwitch to one via `STADO_DEFAULTS_PROVIDER=<name> stado`,\n"+
		"or `/model <name>` to try a specific model in this session.")
	return b.String()
}

// providerErrorHint returns a provider-specific suggestion the user can act
// on. Missing API key → env var + local-alternative pointer; connection
// refused → "start the server" commands.
func providerErrorHint(provider, errMsg string) string {
	switch {
	case strings.Contains(errMsg, "API_KEY not set"):
		env := providerEnvForName(provider)
		return "Fix: `export " + env + "=…` and restart stado, or change\n" +
			"`defaults.provider` in ~/.config/stado/config.toml to one of the\n" +
			"local options: ollama / llamacpp / vllm / lmstudio (no key needed)."
	case strings.Contains(errMsg, "connection refused"):
		return "Fix: start the local server and try again.\n" +
			"  ollama:    `ollama serve`      (→ http://localhost:11434)\n" +
			"  llama.cpp: `llama-server -m …` (→ http://localhost:8080)\n" +
			"  vLLM:      `vllm serve <model>`(→ http://localhost:8000)"
	}
	return ""
}

// providerEnvForName returns the conventional API-key env var for a provider.
func providerEnvForName(p string) string {
	switch p {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "google", "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	case "mistral":
		return "MISTRAL_API_KEY"
	case "cerebras":
		return "CEREBRAS_API_KEY"
	}
	return "the API key env var"
}

// providerDisplayName returns the active provider name, or the configured
// placeholder if the provider hasn't been built yet.
func (m *Model) providerDisplayName() string {
	if m.provider != nil {
		return m.provider.Name()
	}
	return m.providerName
}

// providerCaps returns active capabilities (empty if no provider yet).
func (m *Model) providerCaps() agent.Capabilities {
	if m.provider == nil {
		return agent.Capabilities{}
	}
	return m.provider.Capabilities()
}

// Attach wires the tea.Program so the streaming goroutine can post messages.
func (m *Model) Attach(p *tea.Program) { m.program = p }

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.slash.Width = msg.Width
		m.layout()
		return m, nil

	case streamEventMsg:
		m.handleStreamEvent(msg.ev)
		m.renderBlocks()
		return m, nil

	case streamErrorMsg:
		m.state = stateError
		m.errorMsg = msg.err.Error()
		m.appendBlock(block{kind: "system", body: "error: " + msg.err.Error()})
		m.renderBlocks()
		return m, nil

	case streamDoneMsg:
		m.streamCancel = nil
		return m, m.onTurnComplete()

	case localHintMsg:
		// Async local-runner hint dispatched by ensureProvider's
		// error path. Append as a separate system block so the user
		// sees it arrive after the initial error.
		m.appendBlock(block{kind: "system", body: msg.body})
		m.renderBlocks()
		return m, nil

	case pluginRunResultMsg:
		// /plugin:<name>-<ver> <tool> [args] finished. Render outcome
		// as a system block and leave conversation state untouched —
		// plugin invocations are side-channel and don't pollute the
		// turn log the LLM sees.
		if msg.errMsg != "" {
			m.appendBlock(block{
				kind: "system",
				body: fmt.Sprintf("plugin %s/%s error: %s", msg.plugin, msg.tool, msg.errMsg),
			})
		} else {
			m.appendBlock(block{
				kind: "system",
				body: fmt.Sprintf("plugin %s/%s → %s", msg.plugin, msg.tool, msg.content),
			})
		}
		m.renderBlocks()
		return m, nil

	case toolsExecutedMsg:
		// Append a role=tool message with the accumulated tool results.
		if len(msg.results) > 0 {
			blocks := make([]agent.Block, 0, len(msg.results))
			for _, r := range msg.results {
				cpy := r
				blocks = append(blocks, agent.Block{ToolResult: &cpy})
			}
			m.msgs = append(m.msgs, agent.Message{Role: agent.RoleTool, Content: blocks})
		}
		m.renderBlocks()
		return m, m.startStream()

	case tea.KeyMsg:
		if m.showHelp {
			if m.keys.Matches(msg, keys.SessionInterrupt) || m.keys.Matches(msg, keys.TipsToggle) {
				m.showHelp = false
				m.layout()
			}
			return m, nil
		}

		if m.approval != nil {
			if m.keys.Matches(msg, keys.Approve) {
				return m, m.resolveApproval(true)
			}
			if m.keys.Matches(msg, keys.Deny) {
				return m, m.resolveApproval(false)
			}
			return m, nil
		}

		// Compaction confirmation: reuse the Approve / Deny keybindings
		// (y / n by default) so the UX matches tool-call approval.
		// EditSummary ('e') switches into an inline editor where the
		// user can revise the draft before accepting.
		if m.state == stateCompactionPending {
			if m.keys.Matches(msg, keys.Approve) {
				m.resolveCompaction(true)
				m.renderBlocks()
				return m, nil
			}
			if m.keys.Matches(msg, keys.Deny) {
				m.resolveCompaction(false)
				m.renderBlocks()
				return m, nil
			}
			if m.keys.Matches(msg, keys.EditSummary) {
				m.enterSummaryEdit()
				m.renderBlocks()
				return m, nil
			}
			// Any other key while pending is ignored — no accidental msgs
			// mutation while the user reads the summary.
			return m, nil
		}

		// Summary-editing state: Enter commits, Esc/Deny cancels. All
		// other keys flow to the editor so the user can type freely.
		if m.state == stateCompactionEditing {
			if m.keys.Matches(msg, keys.InputSubmit) {
				m.commitSummaryEdit()
				m.renderBlocks()
				return m, nil
			}
			if m.keys.Matches(msg, keys.Deny) {
				m.cancelSummaryEdit()
				m.renderBlocks()
				return m, nil
			}
			inputCmd, _ := m.input.Update(msg)
			return m, inputCmd
		}

		if m.slash.Visible {
			// Palette owns all keypresses while visible — keystrokes feed
			// its internal Query (so characters don't leak into the main
			// textarea beneath the modal).
			cmd, handled := m.slash.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.slash.Selected(); sel != nil {
					m.slash.Close()
					return m, m.handleSlash(sel.Name)
				}
			}
			// Any other keys swallowed so they don't reach the input.
			return m, nil
		}

		// Model picker is modal too — same routing pattern as palette.
		if m.modelPicker.Visible {
			cmd, handled := m.modelPicker.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.modelPicker.Selected(); sel != nil {
					old := m.model
					oldProvider := m.providerName
					m.model = sel.ID

					// Provider switch: when the selected model came from
					// a different provider (typically a detected local
					// runner), the user almost certainly wants the
					// backend to switch too. Otherwise picking
					// "lmstudio · detected" still routes to anthropic
					// on the next prompt.
					providerSwitched := false
					if sel.ProviderName != "" && sel.ProviderName != oldProvider {
						m.providerName = sel.ProviderName
						m.provider = nil // force rebuild via buildProvider on next ensureProvider
						// Reset the token-counter probe so we re-check
						// against the new backend's capabilities.
						m.tokenCounterChecked = false
						providerSwitched = true
					}

					m.modelPicker.Close()
					body := "model: " + old + " → " + m.model + "  (" + sel.Origin + ")"
					if providerSwitched {
						body += "\nprovider: " + oldProvider + " → " + m.providerName
					}
					m.appendBlock(block{kind: "system", body: body})
					m.layout()
					return m, nil
				}
			}
			return m, nil
		}

		switch {
		case m.keys.Matches(msg, keys.AppExit):
			return m, tea.Quit

		case m.keys.Matches(msg, keys.SidebarToggle):
			m.sidebarOpen = !m.sidebarOpen
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.TipsToggle):
			m.showHelp = true
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.SessionInterrupt):
			if m.state == stateStreaming && m.streamCancel != nil {
				m.streamCancel()
				return m, nil
			}

		case m.keys.Matches(msg, keys.ToolExpand):
			m.toggleLastToolExpand()
			m.renderBlocks()
			return m, nil

		case m.keys.Matches(msg, keys.ModeToggle):
			if m.mode == modeDo {
				m.mode = modePlan
			} else {
				m.mode = modeDo
			}
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.CommandList):
			// Ctrl+P opens the command palette modal. The palette owns
			// its own search input — the main textarea is untouched.
			m.slash.Open()
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.InputClear):
			// Ctrl+C at the top level: cancel in-flight state rather than
			// quit. The exit key is ctrl+d; let ctrl+c act like a
			// "get me out of whatever I was typing" escape that never
			// leaves stado. If the input is empty and nothing's in
			// flight, no-op (user can ctrl+d to exit).
			if m.input.Value() == "" {
				if m.state == stateStreaming && m.streamCancel != nil {
					m.streamCancel()
				}
				if m.state == stateCompactionPending {
					m.resolveCompaction(false)
				}
				if m.approval != nil {
					return m, m.resolveApproval(false)
				}
				return m, nil
			}
			// Non-empty input: the editor's InputClear case (editor.go)
			// resets the textarea. Fall through to let inputCmd do that.

		case m.keys.Matches(msg, keys.InputSubmit):
			if m.input.Value() == "" || m.state == stateStreaming {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			if strings.HasPrefix(text, "/") {
				m.input.Reset()
				m.slash.Visible = false
				return m, m.handleSlash(text)
			}
			// Hard-threshold gate (DESIGN §"Token accounting" 11.2.6).
			// Refuse to start a fresh turn once we're at/above the hard
			// bound — forces the user to /compact or fork before adding
			// more context. The draft text stays in the input so the
			// recovery flow doesn't lose it.
			if m.aboveHardThreshold() {
				m.appendBlock(block{
					kind: "system",
					body: fmt.Sprintf(
						"context at %.0f%% (hard threshold %.0f%%) — blocked. Recover with /compact, or fork from an earlier turn (`stado session fork <id> --at turns/<N>`), then retry.",
						100*m.contextFraction(), 100*m.ctxHardThreshold),
				})
				m.renderBlocks()
				return m, nil
			}
			m.input.History.Push(text)
			m.input.Reset()
			m.appendUser(text)
			m.renderBlocks()
			return m, m.startStream()
		}
	}

	cmd, _ := m.vp, tea.Cmd(nil)
	_ = cmd

	inputCmd, _ := m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	// Scroll messages viewport
	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) View() string {
	if m.showHelp {
		return overlays.RenderHelp(m.keys, m.width)
	}

	sidebarW := 0
	if m.sidebarOpen {
		sidebarW = m.theme.Layout.SidebarWidth
		if sidebarW > m.width/3 {
			sidebarW = m.width / 3
		}
		if sidebarW < m.theme.Layout.SidebarMinWidth {
			m.sidebarOpen = false
			sidebarW = 0
		}
	}
	mainW := m.width - sidebarW
	if sidebarW > 0 {
		mainW -= 1 // gap
	}

	inputH := strings.Count(m.input.Value(), "\n") + 1
	if inputH > m.height/3 {
		inputH = m.height / 3
	}
	// Reserve: input + border(2) + status(1).
	mainH := m.height - inputH - 3
	if m.approval != nil {
		mainH -= 2
	}
	if mainH < 4 {
		mainH = 4
	}

	m.vp.Width = mainW
	m.vp.Height = mainH

	// Left column: messages viewport + approval + input + status
	var left strings.Builder
	left.WriteString(m.vp.View() + "\n")
	if m.approval != nil {
		left.WriteString(m.theme.Fg("warning").Render(
			fmt.Sprintf("⚠ %s — allow? [y]es / [n]o  %s",
				m.approval.toolName, m.theme.Fg("muted").Render(truncate(m.approval.args, mainW-20)))) + "\n")
	}
	left.WriteString(m.renderInputBox(mainW))
	left.WriteString(m.renderStatus(mainW))

	leftBlock := lipgloss.NewStyle().Width(mainW).Render(left.String())

	base := leftBlock
	if sidebarW > 0 {
		sidebar := m.renderSidebar(sidebarW)
		base = lipgloss.JoinHorizontal(lipgloss.Top,
			leftBlock,
			lipgloss.NewStyle().Foreground(m.theme.Fg("border").GetForeground()).Render(strings.Repeat("│\n", m.height-1)+"│"),
			sidebar,
		)
	}

	// Modal overlay: command palette centred on the whole screen.
	if m.slash.Visible {
		m.slash.Width = m.width
		m.slash.Height = m.height
		return m.slash.View(m.width, m.height)
	}
	// Model picker is the second modal — only one can be open at a
	// time since each path routes independently through Update.
	if m.modelPicker.Visible {
		m.modelPicker.Width = m.width
		m.modelPicker.Height = m.height
		return m.modelPicker.View(m.width, m.height)
	}
	return base
}

// layout: recompute viewport size + wrap width; trigger a render.
func (m *Model) layout() {
	m.renderBlocks()
}

// renderInputBox produces the opencode-style bordered input: a textarea
// stacked on top of an inline status line (Mode · Model · Provider),
// all inside one rounded border whose LEFT edge is mode-coloured
// (yellow=Plan, green=Do) so the agent's stance is visible at a glance
// even when focus is elsewhere.
func (m *Model) renderInputBox(mainW int) string {
	inline, err := m.renderer.Exec("input_status", map[string]any{
		"Mode":         m.mode.String(),
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Hint":         "", // reserved — "xhigh" effort-style badge lands when reasoning-effort config does
	})
	if err != nil {
		inline = "[input status render error: " + err.Error() + "]"
	}
	body := m.input.View() + "\n" + strings.TrimRight(inline, "\n")

	borderFg := m.theme.Fg("border").GetForeground()
	modeColor := m.theme.Fg("success").GetForeground() // Do
	if m.mode == modePlan {
		modeColor = m.theme.Fg("warning").GetForeground()
	}
	style := lipgloss.NewStyle().
		Border(m.theme.Border()).
		BorderForeground(borderFg).
		BorderLeftForeground(modeColor).
		Padding(0, 1).
		Width(mainW - 2)
	return style.Render(body) + "\n"
}

// renderStatus runs the bottom status template (right-aligned muted
// tokens/cost + ctrl+p commands hint, plus an optional left-side
// state indicator when busy).
func (m *Model) renderStatus(width int) string {
	state := "idle"
	switch m.state {
	case stateStreaming:
		state = "streaming"
	case stateApproval:
		state = "approval"
	case stateError:
		state = "error"
	}
	tokens := fmt.Sprintf("%s (%s)", humanize(m.usage.InputTokens), tokenPctString(m))
	cost := fmt.Sprintf("$%.2f", m.usage.CostUSD)

	body, err := m.renderer.Exec("status", map[string]any{
		"State":        state,
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Cwd":          m.cwd,
		"ErrorMessage": m.errorMsg,
		"Width":        width,
		"Tokens":       tokens,
		"Cost":         cost,
	})
	if err != nil {
		return fmt.Sprintf("[status render error: %v]", err)
	}
	// Right-align the rendered status within the available width (+ 1 line
	// terminator). The template emits [left-state (optional) + right stats]
	// on a single line; padding the START of the line pushes the whole
	// thing to the right edge. Minus the ANSI noise — strip-free width
	// measurement comes from lipgloss.Width.
	visible := lipgloss.Width(strings.TrimRight(body, "\n"))
	if pad := width - visible; pad > 0 {
		body = strings.Repeat(" ", pad) + strings.TrimRight(body, "\n") + "\n"
	}
	return body
}

// tokenPctString renders the in-context-window fraction for the bottom
// status bar. Returns "0%" when we can't meaningfully compute the ratio.
// Soft/hard thresholds (DESIGN §"Token accounting") colour the number
// when crossed — warning at soft, error at hard — so users see the
// context approaching capacity without reading docs.
// contextFraction returns current input-token usage as a fraction of
// the provider's reported max context. Returns 0 when capacity or
// usage is unknown — callers treat that as "not above threshold".
func (m *Model) contextFraction() float64 {
	cap := m.providerCaps().MaxContextTokens
	used := m.usage.InputTokens
	if cap <= 0 || used == 0 {
		return 0
	}
	return float64(used) / float64(cap)
}

// aboveHardThreshold reports whether the current turn's running
// context usage has crossed the hard threshold. DESIGN §"Token
// accounting" §11.2.6: new user-initiated turns block above this
// bound; in-flight tool-continuation turns are allowed to finish.
func (m *Model) aboveHardThreshold() bool {
	if m.ctxHardThreshold <= 0 {
		return false
	}
	return m.contextFraction() >= m.ctxHardThreshold
}

func tokenPctString(m *Model) string {
	cap := m.providerCaps().MaxContextTokens
	used := m.usage.InputTokens
	if cap <= 0 || used == 0 {
		return "0%"
	}
	fraction := float64(used) / float64(cap)
	s := fmt.Sprintf("%d%%", int(100*fraction))
	switch {
	case fraction >= m.ctxHardThreshold:
		return lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render(s)
	case fraction >= m.ctxSoftThreshold:
		return lipgloss.NewStyle().Foreground(theme.Warning).Bold(true).Render(s)
	}
	return s
}

func (m *Model) renderSidebar(width int) string {
	tokPct := ""
	if cap := m.providerCaps().MaxContextTokens; cap > 0 && m.usage.InputTokens > 0 {
		pct := 100 * m.usage.InputTokens / cap
		tokPct = fmt.Sprintf("%d%% used", pct)
	}
	data := map[string]any{
		"Title":        "stado",
		"Version":      "0.0.0-dev",
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Cwd":          m.cwd,
		"TokensHuman":  fmt.Sprintf("%s tokens", humanize(m.usage.InputTokens+m.usage.OutputTokens)),
		"TokenPercent": tokPct,
		"CostHuman":    fmt.Sprintf("$%.2f spent", m.usage.CostUSD),
		"Todos":        m.todos,
		"Width":        width - 4,
	}
	body, err := m.renderer.Exec("sidebar", data)
	if err != nil {
		body = "[sidebar render error] " + err.Error()
	}
	return m.theme.Pane().Width(width - 2).Height(m.height - 1).Render(body)
}

func (m *Model) renderBlocks() {
	var b strings.Builder
	width := m.vp.Width - 2
	if width < 10 {
		width = 10
	}
	for i, blk := range m.blocks {
		var out string
		var err error
		switch blk.kind {
		case "user":
			out, err = m.renderer.Exec("message_user", map[string]any{
				"Body":  blk.body,
				"Width": width,
			})
		case "assistant":
			out, err = m.renderer.Exec("message_assistant", map[string]any{
				"Body":  blk.body,
				"Width": width,
				"Model": m.model,
			})
		case "thinking":
			out, err = m.renderer.Exec("message_thinking", map[string]any{
				"Body":  blk.body,
				"Width": width,
			})
		case "tool":
			duration := ""
			if !blk.endedAt.IsZero() && !blk.startedAt.IsZero() {
				duration = blk.endedAt.Sub(blk.startedAt).Round(time.Millisecond).String()
			}
			out, err = m.renderer.Exec("message_tool", map[string]any{
				"Name":        blk.toolName,
				"ArgsPreview": truncate(blk.toolArgs, 40),
				"FullArgs":    prettyJSON(blk.toolArgs),
				"Result":      blk.toolResult,
				"Expanded":    blk.expanded,
				"Duration":    duration,
				"Width":       width - 4,
			})
		case "system":
			out = m.theme.Fg("error").Render(blk.body) + "\n"
		}
		if err != nil {
			out = fmt.Sprintf("[render error: %v]\n", err)
		}
		b.WriteString(out)
		if i < len(m.blocks)-1 {
			b.WriteString("\n")
		}
	}
	m.vp.SetContent(b.String())
	m.vp.GotoBottom()
}

// ==== Streaming + conversation state =====================================

func (m *Model) appendUser(text string) {
	m.blocks = append(m.blocks, block{kind: "user", body: text})
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, text))
}

func (m *Model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
}

// startStream fires a non-interactive streaming call to the provider and
// relays events back to the UI via tea.Program.Send.
func (m *Model) startStream() tea.Cmd {
	if !m.ensureProvider() {
		return nil
	}

	// First-turn capability probe (DESIGN §"Token accounting"). A
	// provider that doesn't satisfy TokenCounter means we can't see
	// how close we are to the context window — surface this as a
	// system message so the user knows the context % is unreliable.
	// No hard-block: the compaction recovery path lands in PR D; until
	// then a loud advisory is the best we can do.
	if !m.tokenCounterChecked {
		m.tokenCounterChecked = true
		_, m.tokenCounterPresent = m.provider.(agent.TokenCounter)
		if !m.tokenCounterPresent {
			m.appendBlock(block{
				kind: "system",
				body: fmt.Sprintf("warning: provider %q doesn't expose a token counter — context-window percentage will be zero until the provider returns usage.",
					m.providerDisplayName()),
			})
		}
	}

	// Reset per-turn accumulators.
	m.turnText = ""
	m.turnThinking = ""
	m.turnThinkSig = ""
	m.turnToolCalls = nil

	ctx, cancel := context.WithCancel(context.Background())
	m.streamMu.Lock()
	m.streamCancel = cancel
	m.state = stateStreaming
	m.errorMsg = ""
	m.streamMu.Unlock()

	req := agent.TurnRequest{
		Model:    m.model,
		Messages: m.msgs,
		Tools:    m.toolDefs(),
	}
	// Cache-breakpoint placement — DESIGN §"Prompt-cache awareness".
	// One ephemeral breakpoint on the last prior message, so every turn
	// caches the entire history up through the previous turn.
	if m.provider.Capabilities().SupportsPromptCache && len(m.msgs) > 0 {
		req.CacheHints = []agent.CachePoint{{MessageIndex: len(m.msgs) - 1}}
	}

	go func() {
		defer cancel()
		ch, err := m.provider.StreamTurn(ctx, req)
		if err != nil {
			m.sendMsg(streamErrorMsg{err: err})
			return
		}
		for ev := range ch {
			m.sendMsg(streamEventMsg{ev: ev})
			if ev.Kind == agent.EvDone || ev.Kind == agent.EvError {
				if ev.Kind == agent.EvDone && ev.Usage != nil {
					// InputTokens is the prompt size for this turn, already
					// including all prior history — assign, don't accumulate.
					// DESIGN §"Token accounting" threshold percentages are
					// relative to this number.
					m.usage.InputTokens = ev.Usage.InputTokens
					m.usage.OutputTokens += ev.Usage.OutputTokens
					m.usage.CostUSD += ev.Usage.CostUSD
				}
				m.sendMsg(streamDoneMsg{})
				return
			}
		}
		m.sendMsg(streamDoneMsg{})
	}()
	return nil
}

func (m *Model) sendMsg(msg tea.Msg) {
	if m.program != nil {
		m.program.Send(msg)
	}
}

func (m *Model) handleStreamEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EvTextDelta:
		// Compaction streams go into the pending-summary buffer AND the
		// assistant block the caller pre-appended — the user sees the
		// summary materialise, and resolveCompaction has the full text
		// when they accept.
		if m.compacting {
			m.pendingCompactionSummary += ev.Text
			if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == "assistant" {
				m.blocks[len(m.blocks)-1].body += ev.Text
			}
			return
		}
		m.turnText += ev.Text
		if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "assistant" {
			m.blocks = append(m.blocks, block{kind: "assistant"})
		}
		m.blocks[len(m.blocks)-1].body += ev.Text

	case agent.EvThinkingDelta:
		m.turnThinking += ev.Text
		m.turnThinkSig += ev.ThinkingSig
		if ev.Text != "" {
			if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "thinking" {
				m.blocks = append(m.blocks, block{kind: "thinking"})
			}
			m.blocks[len(m.blocks)-1].body += ev.Text
		}

	case agent.EvToolCallStart:
		if ev.ToolCall == nil {
			return
		}
		m.blocks = append(m.blocks, block{
			kind:      "tool",
			toolID:    ev.ToolCall.ID,
			toolName:  ev.ToolCall.Name,
			startedAt: time.Now(),
		})

	case agent.EvToolCallArgsDelta:
		if len(m.blocks) == 0 {
			return
		}
		last := &m.blocks[len(m.blocks)-1]
		if last.kind == "tool" {
			last.toolArgs += ev.ToolArgsDelta
		}

	case agent.EvToolCallEnd:
		if ev.ToolCall == nil {
			return
		}
		cp := *ev.ToolCall
		m.turnToolCalls = append(m.turnToolCalls, cp)
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == "tool" && m.blocks[i].toolID == ev.ToolCall.ID {
				m.blocks[i].toolArgs = string(ev.ToolCall.Input)
				m.blocks[i].endedAt = time.Now()
				break
			}
		}
	}
}

// onTurnComplete is called when the provider's stream ends. It persists the
// assistant turn into msgs; if the turn ended on tool calls, it starts the
// approval queue so the user sees each tool before it runs.
func (m *Model) onTurnComplete() tea.Cmd {
	// Compaction turn: the summariser has produced its draft. Park in
	// stateCompactionPending, waiting for y/n. msgs is NOT touched — the
	// replacement only happens after explicit confirmation.
	if m.compacting {
		m.compacting = false
		if strings.TrimSpace(m.pendingCompactionSummary) == "" {
			m.appendBlock(block{kind: "system", body: "compaction: model returned empty summary — aborting."})
			m.state = stateIdle
			return nil
		}
		m.appendBlock(block{
			kind: "system",
			body: "compaction: press 'y' to replace conversation with the summary above, 'n' to discard.",
		})
		m.state = stateCompactionPending
		return nil
	}

	// Build the assistant message from the accumulated turn.
	var asstBlocks []agent.Block
	if m.turnThinking != "" || m.turnThinkSig != "" {
		asstBlocks = append(asstBlocks, agent.Block{Thinking: &agent.ThinkingBlock{
			Text:      m.turnThinking,
			Signature: m.turnThinkSig,
		}})
	}
	if m.turnText != "" {
		asstBlocks = append(asstBlocks, agent.Block{Text: &agent.TextBlock{Text: m.turnText}})
	}
	for i := range m.turnToolCalls {
		tc := m.turnToolCalls[i]
		asstBlocks = append(asstBlocks, agent.Block{ToolUse: &tc})
	}
	if len(asstBlocks) > 0 {
		m.msgs = append(m.msgs, agent.Message{Role: agent.RoleAssistant, Content: asstBlocks})
	}

	if len(m.turnToolCalls) == 0 {
		m.state = stateIdle
		return nil
	}

	m.pendingCalls = append([]agent.ToolUseBlock{}, m.turnToolCalls...)
	m.pendingResults = nil
	return m.advanceApproval()
}

// advanceApproval pops the next pending call and either auto-executes
// (remembered allow) or shows an approval prompt. When the queue drains it
// returns a tea.Cmd that posts toolsExecutedMsg with the accumulated results.
func (m *Model) advanceApproval() tea.Cmd {
	for len(m.pendingCalls) > 0 {
		call := m.pendingCalls[0]
		if m.rememberedAllow[call.Name] {
			m.pendingCalls = m.pendingCalls[1:]
			m.pendingResults = append(m.pendingResults, m.executeCall(call))
			continue
		}
		m.state = stateApproval
		m.approval = &approvalRequest{
			toolName: call.Name,
			toolID:   call.ID,
			args:     string(call.Input),
		}
		m.renderBlocks()
		return nil
	}
	// Queue drained — post the results and let the agent loop re-stream.
	results := m.pendingResults
	m.pendingResults = nil
	m.state = stateIdle
	return func() tea.Msg { return toolsExecutedMsg{results: results} }
}

// executeCall actually runs a single tool through the Executor. Called after
// approve/remembered-allow. Returns a ToolResultBlock suitable for append
// to pendingResults.
func (m *Model) executeCall(call agent.ToolUseBlock) agent.ToolResultBlock {
	if m.executor == nil {
		return agent.ToolResultBlock{
			ToolUseID: call.ID,
			Content:   "tool execution unavailable (no session)",
			IsError:   true,
		}
	}
	workdir := m.cwd
	if m.session != nil {
		workdir = m.session.WorktreePath
	}
	res, err := m.executor.Run(context.Background(), call.Name, call.Input, hostAdapter{
		workdir: workdir,
		readLog: m.executor.ReadLog,
	})
	content := res.Content
	isErr := res.Error != ""
	if err != nil {
		content = err.Error()
		isErr = true
	} else if isErr {
		content = res.Error
	}
	return agent.ToolResultBlock{ToolUseID: call.ID, Content: content, IsError: isErr}
}

// toolDefs builds the tool-definition list for the current turn request. An
// empty registry (no session) returns nil so the provider runs pure chat.
//
// In Plan mode only NonMutating tools are exposed — the model can grep/read/
// look-up-defs to form a plan, but can't edit/write/bash. This is the
// principled enforcement (no approval-loop workaround): the model literally
// doesn't see the mutating tools as available, so it produces analysis
// rather than asking to execute.
func (m *Model) toolDefs() []agent.ToolDef {
	if m.executor == nil {
		return nil
	}
	all := m.executor.Registry.All()
	out := make([]agent.ToolDef, 0, len(all))
	for _, t := range all {
		if m.mode == modePlan {
			class := m.executor.Registry.ClassOf(t.Name())
			if class != tool.ClassNonMutating {
				continue
			}
		}
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

// compactRequest / compactReplace are thin aliases so the code sites
// read in-place (the compact package owns the wire contract, not the TUI).
var (
	compactRequest = compact.BuildRequest
	compactReplace = compact.ReplaceMessages
)

// renderContextStatus summarises what the ctx% in the status bar is
// made of, plus what the user's options are at each threshold. Kept
// terse — one system block, readable in < 1 screen.
func (m *Model) renderContextStatus() string {
	used := m.usage.InputTokens
	var sb strings.Builder

	caps := m.providerCaps()
	switch {
	case !m.tokenCounterPresent && m.tokenCounterChecked:
		sb.WriteString(fmt.Sprintf("context: unavailable — provider %q doesn't expose a token counter.\n",
			m.providerDisplayName()))
	case caps.MaxContextTokens == 0:
		sb.WriteString("context: unavailable — provider hasn't reported MaxContextTokens.\n")
	case used == 0:
		sb.WriteString(fmt.Sprintf("context: 0 / %d tokens (0%%) — first turn hasn't run yet.\n",
			caps.MaxContextTokens))
	default:
		fraction := float64(used) / float64(caps.MaxContextTokens)
		sb.WriteString(fmt.Sprintf("context: %s / %s tokens (%.1f%%)\n",
			humanize(used), humanize(caps.MaxContextTokens), 100*fraction))
		sb.WriteString(fmt.Sprintf("thresholds: soft %.0f%% · hard %.0f%%\n",
			100*m.ctxSoftThreshold, 100*m.ctxHardThreshold))
		switch {
		case fraction >= m.ctxHardThreshold:
			sb.WriteString("status: above hard threshold — consider /compact or `stado session fork <id> --at turns/<N>` in another shell.\n")
		case fraction >= m.ctxSoftThreshold:
			sb.WriteString("status: above soft threshold — forking from an earlier turn is the preferred recovery; /compact is the lossy fallback.\n")
		default:
			sb.WriteString("status: healthy.\n")
		}
	}
	sb.WriteString(fmt.Sprintf("turns: %d messages in history\n", len(m.msgs)))
	sb.WriteString("options: /compact (summarise + confirm)  ·  session tree <id> / session fork <id> --at turns/<N>")
	return strings.TrimRight(sb.String(), "\n")
}

// startCompaction kicks off a summarisation stream and parks the UI in
// stateCompactionPending once it completes. See DESIGN §"Compaction":
// user-invoked only, explicit confirmation required before msgs is
// replaced.
func (m *Model) startCompaction() tea.Cmd {
	if m.state != stateIdle {
		m.appendBlock(block{kind: "system", body: "compaction: busy — wait for the current turn to finish"})
		return nil
	}
	if !m.ensureProvider() {
		return nil
	}
	if len(m.msgs) == 0 {
		m.appendBlock(block{kind: "system", body: "compaction: conversation is empty — nothing to compact"})
		return nil
	}

	m.appendBlock(block{kind: "system", body: "compacting conversation — streaming proposed summary below..."})
	m.appendBlock(block{kind: "assistant", body: ""})
	// Remember where the streamed summary lives so inline-edit
	// ('e' key) can rewrite the right block when the user revises.
	m.compactionBlockIdx = len(m.blocks) - 1
	m.compacting = true
	m.pendingCompactionSummary = ""

	ctx, cancel := context.WithCancel(context.Background())
	m.streamMu.Lock()
	m.streamCancel = cancel
	m.state = stateStreaming
	m.errorMsg = ""
	m.streamMu.Unlock()

	req := compactRequest(m.model, m.msgs)

	go func() {
		defer cancel()
		ch, err := m.provider.StreamTurn(ctx, req)
		if err != nil {
			m.sendMsg(streamErrorMsg{err: err})
			return
		}
		for ev := range ch {
			m.sendMsg(streamEventMsg{ev: ev})
			if ev.Kind == agent.EvDone || ev.Kind == agent.EvError {
				m.sendMsg(streamDoneMsg{})
				return
			}
		}
		m.sendMsg(streamDoneMsg{})
	}()
	return nil
}

// enterSummaryEdit swaps the user's in-flight draft for the proposed
// compaction summary so they can revise it in the main editor. The
// draft is stashed and restored on commit/cancel — DESIGN §"Compaction"
// emphasises the user shouldn't lose their current thought while
// deciding how to recover.
func (m *Model) enterSummaryEdit() {
	if m.state != stateCompactionPending {
		return
	}
	m.savedDraftBeforeEdit = m.input.Value()
	m.input.SetValue(m.pendingCompactionSummary)
	m.state = stateCompactionEditing
	m.appendBlock(block{
		kind: "system",
		body: "editing summary — Enter to save, Esc/n to cancel.",
	})
}

// commitSummaryEdit finalises the edit: the new text becomes
// pendingCompactionSummary AND is written back into the visible
// assistant block so the user sees the revision before pressing y.
func (m *Model) commitSummaryEdit() {
	if m.state != stateCompactionEditing {
		return
	}
	edited := m.input.Value()
	m.pendingCompactionSummary = edited
	if m.compactionBlockIdx >= 0 && m.compactionBlockIdx < len(m.blocks) {
		m.blocks[m.compactionBlockIdx].body = edited
	}
	m.input.SetValue(m.savedDraftBeforeEdit)
	m.savedDraftBeforeEdit = ""
	m.state = stateCompactionPending
	m.appendBlock(block{
		kind: "system",
		body: "summary updated — press 'y' to apply, 'n' to discard, 'e' to edit again.",
	})
}

// cancelSummaryEdit restores the original summary + the draft the user
// had in flight. pendingCompactionSummary and the visible block are
// left untouched — we only discard the editor's buffer.
func (m *Model) cancelSummaryEdit() {
	if m.state != stateCompactionEditing {
		return
	}
	m.input.SetValue(m.savedDraftBeforeEdit)
	m.savedDraftBeforeEdit = ""
	m.state = stateCompactionPending
	m.appendBlock(block{
		kind: "system",
		body: "edit cancelled — original summary kept.",
	})
}

// resolveCompaction is called from Update when the user presses 'y' or
// 'n' while in stateCompactionPending. 'y' replaces msgs AND writes a
// dual-ref git commit (tree + trace) recording the compaction event;
// 'n' discards the summary and leaves both sides untouched.
//
// DESIGN §"Compaction" invariant: `tree` commit keeps its parent's
// tree hash (filesystem unchanged — compaction is conversation-scope,
// not file-scope), so `git checkout refs/sessions/<id>/tree~1 -- …`
// restores the pre-compaction state exactly. `trace` keeps the raw
// turn commits so the audit trail is never rewritten.
func (m *Model) resolveCompaction(accept bool) {
	if m.state != stateCompactionPending {
		return
	}
	if accept {
		summary := m.pendingCompactionSummary
		turnsTotal := len(m.msgs)
		m.msgs = compactReplace(summary)

		accepted := "compaction accepted — prior conversation replaced with summary."
		if m.session != nil {
			toTurn := m.session.Turn()
			title := compactionTitle(summary)
			treeSHA, traceSHA, err := m.session.CommitCompaction(stadogit.CompactionMeta{
				Title:      title,
				Summary:    summary,
				FromTurn:   0, // chained-compactions tracking lands in a follow-up
				ToTurn:     toTurn,
				TurnsTotal: turnsTotal,
				ByAuthor:   m.providerDisplayName(),
			})
			if err != nil {
				accepted += fmt.Sprintf("\n(tree/trace commit failed: %v — summary still replaced in-memory.)", err)
			} else {
				accepted += fmt.Sprintf("\ntree: %s  trace: %s",
					treeSHA.String()[:12], traceSHA.String()[:12])
			}
		}
		m.appendBlock(block{kind: "system", body: accepted})
	} else {
		m.appendBlock(block{kind: "system", body: "compaction declined — conversation unchanged."})
	}
	m.pendingCompactionSummary = ""
	m.state = stateIdle
}

// compactionTitle derives a short subject line from the summary — the
// first sentence, capped at ~70 chars. The full body lands in the
// commit message under the subject.
func compactionTitle(summary string) string {
	s := strings.TrimSpace(summary)
	if i := strings.IndexAny(s, ".\n"); i > 0 && i < 120 {
		s = s[:i]
	}
	if len(s) > 70 {
		s = s[:69] + "…"
	}
	return s
}

// hostAdapter implements tool.Host for the executor goroutine. Approval is
// auto-allow in this build; PLAN §5 introduces the real approval flow.
// readLog delegates PriorRead/RecordRead to the Executor's shared log so
// the read tool can dedup across a session's turns.
type hostAdapter struct {
	workdir string
	readLog *tools.ReadLog
}

func (h hostAdapter) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h hostAdapter) Workdir() string { return h.workdir }

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

func (m *Model) toggleLastToolExpand() {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "tool" {
			m.blocks[i].expanded = !m.blocks[i].expanded
			return
		}
	}
}

func (m *Model) resolveApproval(allow bool) tea.Cmd {
	req := m.approval
	m.approval = nil
	if req == nil || len(m.pendingCalls) == 0 {
		m.state = stateIdle
		m.renderBlocks()
		return nil
	}
	call := m.pendingCalls[0]
	m.pendingCalls = m.pendingCalls[1:]

	if allow {
		m.pendingResults = append(m.pendingResults, m.executeCall(call))
	} else {
		m.pendingResults = append(m.pendingResults, agent.ToolResultBlock{
			ToolUseID: call.ID,
			Content:   "Denied by user",
			IsError:   true,
		})
	}
	return m.advanceApproval()
}

func (m *Model) handleSlash(text string) tea.Cmd {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil
	}
	// /plugin and /plugin:<name>-<ver> [<tool> [json-args]] — routed
	// before the switch since the plugin-name suffix is dynamic.
	if parts[0] == "/plugin" || strings.HasPrefix(parts[0], "/plugin:") {
		return m.handlePluginSlash(parts)
	}
	switch parts[0] {
	case "/clear":
		m.blocks = nil
		m.msgs = nil
		m.renderBlocks()
	case "/help":
		m.showHelp = true
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
		if len(parts) >= 2 && parts[1] == "forget" {
			m.rememberedAllow = nil
			m.appendBlock(block{kind: "system", body: "remembered approvals cleared"})
		} else if len(parts) >= 3 && parts[1] == "always" {
			if m.rememberedAllow == nil {
				m.rememberedAllow = map[string]bool{}
			}
			m.rememberedAllow[parts[2]] = true
			m.appendBlock(block{kind: "system", body: "will auto-approve " + parts[2] + " for the rest of this session"})
		} else {
			m.appendBlock(block{kind: "system", body: "usage: /approvals always <tool>  |  /approvals forget"})
		}
	case "/tools":
		if m.executor == nil {
			m.appendBlock(block{kind: "system", body: "no tools registered (session unavailable)"})
		} else {
			var sb strings.Builder
			sb.WriteString("Registered tools:")
			for _, t := range m.executor.Registry.All() {
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
	default:
		m.appendBlock(block{kind: "system", body: "unknown command: " + parts[0] + " (try /help)"})
	}
	m.layout()
	return nil
}

// handlePluginSlash routes `/plugin` and `/plugin:<name>-<ver>` forms:
//
//   /plugin                                      → list installed plugins
//   /plugin:<name>-<ver>                         → list that plugin's tools
//   /plugin:<name>-<ver> <tool>                  → run with args={}
//   /plugin:<name>-<ver> <tool> {"key":"val",…}  → run with supplied JSON
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
	ts := plugins.NewTrustStore(cfg.StateDir())
	if err := ts.VerifyManifest(mf, sig); err != nil {
		m.appendBlock(block{kind: "system", body: "plugin signature: " + err.Error()})
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

	return runPluginToolAsync(pluginDir, mf, *tdef, argsJSON, nameVer)
}

// runPluginToolAsync spawns a fresh wazero runtime, instantiates the
// module under its capability-bound Host, invokes the tool, and posts
// the outcome back via pluginRunResultMsg. Hard-capped at 30s so a
// runaway plugin can't wedge the TUI.
func runPluginToolAsync(dir string, mf *plugins.Manifest, tdef plugins.ToolDef, argsJSON, pluginID string) tea.Cmd {
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

// ---- helpers --------------------------------------------------------------

func humanize(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

func truncate(s string, max int) string {
	if max <= 1 || len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func prettyJSON(raw string) string {
	if raw == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return string(out)
}
