package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/input"
	"github.com/foobarto/stado/internal/tui/keys"
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
	toolErr    bool
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
	streamEventMsg    struct{ ev agent.Event }
	streamErrorMsg    struct{ err error }
	streamDoneMsg     struct{}
	approvalMsg       struct{ allow bool }
	toolsExecutedMsg  struct{ results []agent.ToolResultBlock }
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
	input    *input.Editor
	slash    *palette.Model
	vp       viewport.Model
	showHelp bool

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

	// Aggregate usage across turns
	usage agent.Usage

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
		cwd:           cwd,
		keys:          keyReg,
		theme:         rnd.Theme(),
		renderer:      rnd,
		buildProvider: buildProvider,
		providerName:  providerName,
		model:         modelName,
		input:         input.New(keyReg),
		slash:         palette.New(),
		vp:            viewport.New(0, 0),
		sidebarOpen:   true,
	}
	return m
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
		m.appendBlock(block{kind: "system", body: body})
		return false
	}
	m.provider = p
	return true
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
			if m.input.Value() == "" {
				return m, tea.Quit
			}

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
	inner := mainW - 4 // border + padding
	if inner < 20 {
		inner = 20
	}
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
	tokens := fmt.Sprintf("%s (%s)", humanize(m.usage.InputTokens+m.usage.OutputTokens), tokenPctString(m))
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
// status bar. Returns "" when we can't meaningfully compute the ratio.
func tokenPctString(m *Model) string {
	cap := m.providerCaps().MaxContextTokens
	used := m.usage.InputTokens + m.usage.OutputTokens
	if cap <= 0 || used == 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", 100*used/cap)
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
					m.usage.InputTokens += ev.Usage.InputTokens
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
			m.appendBlock(block{kind: "system", body: "current model: " + m.model + "  (usage: /model <name>)"})
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
	default:
		m.appendBlock(block{kind: "system", body: "unknown command: " + parts[0] + " (try /help)"})
	}
	m.layout()
	return nil
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
