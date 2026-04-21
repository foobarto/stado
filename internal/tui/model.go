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
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tui/banner"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/filepicker"
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
	// pluginForkMsg is dispatched when a plugin's ForkFn closure
	// creates a child session. Surfaced in Update as a user-visible
	// system block per DESIGN invariant 4 — "user-visible by default."
	pluginForkMsg struct {
		plugin    string // plugin name from the manifest
		childID   string // new session ID
		atTurnRef string // fork point, or empty for parent tree HEAD
		seed      string // plugin-provided seed / summary text
	}
)

// Model is the root bubbletea model for stado's TUI.
type Model struct {
	// Config + infrastructure
	cwd      string
	keys     *keys.Registry
	theme    *theme.Theme
	renderer *render.Renderer

	// rootCtx is the ancestor context for every span this TUI
	// creates. When cwd contains a `.stado-span-context` (written by
	// a prior `stado session fork`), it carries the parent trace
	// reference so the TUI's spans link back — Phase 9.4/9.5 cross-
	// process span link. Defaults to context.Background() in the
	// no-fork-link case.
	rootCtx context.Context

	// backgroundPlugins are persistent plugin instances loaded once
	// per TUI session from cfg.Plugins.Background. Each ticks after
	// every turn boundary so session-observing plugins (auto-compact,
	// telemetry bridges, recorders) can react without needing an
	// explicit user slash-command. See internal/plugins/runtime
	// §BackgroundPlugin for the ABI contract.
	backgroundPlugins []*pluginRuntime.BackgroundPlugin
	// pluginRuntime shared across all background plugins — each
	// plugin's Module is separate, but the wazero Runtime is the
	// container. Nil until LoadBackgroundPlugins runs.
	bgPluginRuntime *pluginRuntime.Runtime

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
	filePicker  *filepicker.Model
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

	// queuedPrompt is the user's follow-up message buffered while an
	// earlier turn is still streaming. Enter while stateStreaming
	// enqueues; onTurnComplete drains. Esc / Ctrl+C clears the queue
	// (preferred over cancelling the in-flight stream when a queued
	// message exists). Pi's pattern — lets the user line up "the next
	// thing to try" without waiting for the model to finish typing.
	queuedPrompt string

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

	// Budget thresholds from config.Budget. Compared against
	// usage.CostUSD (cumulative across turns).
	// - budgetWarnUSD > 0 and crossed: render "budget N%" pill and
	//   append a one-time system block. budgetWarned latches so the
	//   block doesn't repeat every turn.
	// - budgetHardUSD > 0 and crossed: further user prompts are
	//   blocked with an actionable hint; session acks to unblock via
	//   `/budget ack` which sets budgetAcked for the rest of the
	//   session.
	budgetWarnUSD float64
	budgetHardUSD float64
	budgetWarned  bool
	budgetAcked   bool

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

	// systemPrompt is the project-root AGENTS.md / CLAUDE.md body
	// resolved at TUI startup. Injected into every TurnRequest.System
	// so the model sees project-specific guidance without the user
	// having to paste it into every session. Empty if no file was
	// found walking up from cwd.
	systemPrompt     string
	systemPromptPath string

	// skills is the list of `.stado/skills/*.md` files discovered at
	// startup. Each is reachable as `/skill:<name>` from the palette;
	// invocation injects the skill body as a user message so the
	// LLM acts on it. Empty when no skills dir exists up the tree.
	skills []skills.Skill

	// hookRunner fires user-configured shell hooks at lifecycle
	// events (see config.Hooks). Zero-value is a no-op so the TUI
	// boots fine without any hooks defined.
	hookRunner hooks.Runner
	// turnStart timestamps the moment we called startStream, so the
	// post_turn hook can report wall-clock duration.
	turnStart time.Time
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
		filePicker:       filepicker.New(),
		vp:               viewport.New(0, 0),
		sidebarOpen:      true,
		ctxSoftThreshold: 0.70, // DESIGN §"Token accounting" defaults.
		ctxHardThreshold: 0.90,
		rootCtx:          context.Background(),
	}
	// Load project-root instructions (AGENTS.md preferred, CLAUDE.md
	// fallback). A missing file is fine; a broken file is a stderr
	// warning — we'd rather boot the TUI with no system prompt than
	// refuse to start.
	if res, err := instructions.Load(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "stado: instructions load: %v\n", err)
	} else if res.Content != "" {
		m.systemPrompt = res.Content
		m.systemPromptPath = res.Path
	}
	// Load project-level skills (`.stado/skills/*.md` up the tree).
	// A broken skill file surfaces as a stderr warning alongside any
	// successfully-loaded skills — one bad file shouldn't hide the
	// others.
	if sks, err := skills.Load(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "stado: skills load: %v\n", err)
		m.skills = sks
	} else {
		m.skills = sks
	}
	return m
}

// SetRootContext replaces the TUI's ancestor context. Called from the
// entry point (tui.Run) with a context pre-wrapped with any
// `.stado-span-context` present in cwd — makes fork-triggered child
// processes link back to the parent trace (Phase 9.4/9.5). Safe to
// call before the bubbletea program starts.
func (m *Model) SetRootContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.rootCtx = ctx
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

// SetHooks propagates [hooks] config into the TUI. Passing an empty
// string disables a given hook. Called from app.go; tests can set
// hooks directly on the model.
func (m *Model) SetHooks(postTurn string) {
	m.hookRunner.PostTurnCmd = postTurn
}

// SetBudget propagates [budget] config into the TUI. Both args are
// optional (zero = "no cap"); a negative value is a no-op. Sanity
// check (hard > warn) is enforced upstream in config.Load.
func (m *Model) SetBudget(warnUSD, hardUSD float64) {
	if warnUSD >= 0 {
		m.budgetWarnUSD = warnUSD
	}
	if hardUSD >= 0 {
		m.budgetHardUSD = hardUSD
	}
}

// budgetExceeded reports whether the cumulative session cost has
// crossed the configured hard cap. budgetAcked lets the user continue
// past the cap for the rest of the session after confirming.
func (m *Model) budgetExceeded() bool {
	if m.budgetAcked || m.budgetHardUSD <= 0 {
		return false
	}
	return m.usage.CostUSD >= m.budgetHardUSD
}

// budgetWarning returns a short status-bar pill ("budget $X / $Y") when
// cumulative cost has crossed warn but not hard. Empty when no warn cap
// is configured or cost is still below it.
func (m *Model) budgetWarning() string {
	if m.budgetWarnUSD <= 0 || m.usage.CostUSD < m.budgetWarnUSD {
		return ""
	}
	cap := m.budgetWarnUSD
	if m.budgetHardUSD > 0 {
		cap = m.budgetHardUSD
	}
	return fmt.Sprintf("budget $%.2f/$%.2f", m.usage.CostUSD, cap)
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

// renderSessionsOverview is the backing formatter for the `/sessions`
// slash command. Enumerates every other session for the current repo
// with last-active time, turn/message/compaction counts, and a
// `stado session resume <id>` hint per row.
//
// Swapping sessions live inside a running TUI isn't supported (m.msgs
// + m.session are tied to the program's lifecycle), so the output is
// informational — the user exits + runs resume to switch.
func (m *Model) renderSessionsOverview() string {
	if m.session == nil || m.session.Sidecar == nil {
		return "/sessions: no live session — run `stado session list` instead."
	}
	worktreeRoot := filepath.Dir(m.session.WorktreePath)
	sc := m.session.Sidecar

	// Scan sidecar refs for all session IDs. Same pattern
	// `stado session list` uses.
	ids := map[string]struct{}{}
	iter, err := sc.Repo().References()
	if err != nil {
		return "/sessions: could not list session refs: " + err.Error()
	}
	defer iter.Close()
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		const prefix = "refs/sessions/"
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		rest := strings.TrimPrefix(name, prefix)
		id := strings.Split(rest, "/")[0]
		ids[id] = struct{}{}
		return nil
	})
	// Augment with worktree-only sessions (never-committed forks).
	if entries, err := os.ReadDir(worktreeRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				ids[e.Name()] = struct{}{}
			}
		}
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
	b.WriteString("\nOther sessions:\n")
	for _, id := range sorted {
		r := runtime.SummariseSession(worktreeRoot, sc, id)
		label := r.ID
		if r.Description != "" {
			label = fmt.Sprintf("%s  \"%s\"", r.ID, r.Description)
		}
		fmt.Fprintf(&b, "  %s\n", label)
		fmt.Fprintf(&b, "    %s  turns=%d msgs=%d compact=%d  %s\n",
			r.LastActiveFormatted(), r.Turns, r.Msgs, r.Compactions, r.Status)
		fmt.Fprintf(&b, "    resume:  stado session resume %s\n", r.ID)
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
		// Budget warn-once check: m.usage.CostUSD was updated inside
		// the stream goroutine before sendMsg(streamDoneMsg), so by
		// the time we're here it reflects the just-finished turn.
		m.maybeEmitBudgetWarning()
		// Fire the post_turn lifecycle hook (no-op when unset). Runs
		// synchronously but capped at 5s inside the Runner so a slow
		// hook can't stall the next turn meaningfully.
		m.firePostTurnHook()
		// Turn-boundary event for background plugins. Emit a
		// turn_complete event onto every plugin's bridge so polling
		// session:observe consumers see it, then tick each plugin.
		m.emitTurnCompleteToBridges()
		return m, tea.Batch(m.onTurnComplete(), m.tickBackgroundPlugins())

	case backgroundTickResultMsg:
		m.backgroundPlugins = msg.survivors
		return m, nil

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

	case pluginForkMsg:
		// A plugin's session:fork capability just created a child
		// session. DESIGN invariant 4: this is user-visible by
		// default. Show both the new session id + the fork point +
		// a summary of the seed the plugin wrote into the child's
		// trace log.
		at := msg.atTurnRef
		if at == "" {
			at = "parent tree HEAD"
		}
		body := fmt.Sprintf("plugin %s forked session → %s  (at %s)", msg.plugin, msg.childID, at)
		if msg.seed != "" {
			body += "\n  seed: " + trimSeed(msg.seed, 120)
		}
		body += "\n  attach:  stado session attach " + msg.childID
		m.appendBlock(block{kind: "system", body: body})
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
			toolMsg := agent.Message{Role: agent.RoleTool, Content: blocks}
			m.msgs = append(m.msgs, toolMsg)
			m.persistMessage(toolMsg)
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

		// Filepicker popover owns navigation keys while visible so
		// Up/Down don't scroll the textarea and Tab/Enter accept the
		// highlighted path instead of inserting literal whitespace or
		// submitting a half-written prompt. Esc closes without
		// inserting. Anything else falls through so typing refines
		// the query naturally.
		if m.filePicker.Visible {
			if cmd, handled := m.filePicker.Update(msg); handled {
				return m, cmd
			}
			switch msg.Type {
			case tea.KeyEsc:
				m.filePicker.Close()
				return m, nil
			case tea.KeyTab:
				m.acceptFilePickerSelection()
				return m, nil
			case tea.KeyEnter:
				if m.filePicker.Selected() != "" {
					m.acceptFilePickerSelection()
					return m, nil
				}
			}
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
				// Queued-prompt clears first: if the user queued a
				// follow-up while streaming and wants to take it back,
				// they reach for Ctrl+C/Esc before the model finishes.
				// Don't also cancel the stream in the same keystroke —
				// that combines two intents.
				if m.queuedPrompt != "" {
					m.queuedPrompt = ""
					return m, nil
				}
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
			if m.input.Value() == "" {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			// Enter while a turn is still streaming: queue the prompt
			// for after-done instead of silently dropping it (the old
			// behaviour) or abruptly cancelling (bad UX). The user's
			// block is appended to m.blocks IMMEDIATELY so they see
			// their message in the chat (dogfood-bug: silent queue
			// looked like a freeze). Only m.msgs add + startStream
			// wait for drain — the current turn is mid-stream and
			// must not see the new user message in its context window.
			if m.state == stateStreaming {
				m.queuedPrompt = text
				if !strings.HasPrefix(text, "/") {
					m.appendBlock(block{kind: "user", body: text})
					m.renderBlocks()
				}
				m.input.History.Push(text)
				m.input.Reset()
				return m, nil
			}
			if strings.HasPrefix(text, "/") {
				m.input.Reset()
				m.slash.Visible = false
				return m, m.handleSlash(text)
			}
			// Budget hard-cap gate (same UX pattern as the context
			// hard-threshold). Draft text stays in input; user clears
			// the block with `/budget ack` which sets budgetAcked for
			// the remainder of the session.
			if m.budgetExceeded() {
				body := fmt.Sprintf(
					"cost $%.2f ≥ hard cap $%.2f — blocked. Continue with:\n"+
						"  · /budget ack — acknowledge and continue this session\n"+
						"  · edit [budget].hard_usd in config.toml to raise the cap",
					m.usage.CostUSD, m.budgetHardUSD)
				m.appendBlock(block{kind: "system", body: body})
				m.renderBlocks()
				return m, nil
			}
			// Hard-threshold gate (DESIGN §"Token accounting" 11.2.6).
			// Refuse to start a fresh turn once we're at/above the hard
			// bound — forces the user to /compact or fork before adding
			// more context. The draft text stays in the input so the
			// recovery flow doesn't lose it.
			if m.aboveHardThreshold() {
				body := fmt.Sprintf(
					"context at %.0f%% (hard threshold %.0f%%) — blocked. Recover with:\n"+
						"  · /compact — user-confirmed in-TUI summarisation\n"+
						"  · stado session fork <id> --at turns/<N> — branch from an earlier turn",
					100*m.contextFraction(), 100*m.ctxHardThreshold)
				// Offer auto-compact specifically when it's installed —
				// the user doesn't have to remember the exact plugin-id
				// string; we've already found one on disk.
				if ac := m.installedAutoCompact(); ac != "" {
					body += fmt.Sprintf("\n  · /plugin:%s compact — automated compact + fork via the auto-compact plugin", ac)
				}
				m.appendBlock(block{kind: "system", body: body})
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

	// Re-scan for an active @-trigger after every editor keypress.
	// Typing '@' opens the picker; typing past the word boundary
	// (space, newline, or moving the cursor away) closes it.
	m.updateFilePickerFromInput()

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
		// Too-narrow terminal: don't render a sidebar this frame, but
		// keep m.sidebarOpen so a later WindowSizeMsg with a wider
		// terminal brings it back. Previously we flipped the flag here,
		// which meant the first View() call (pre-WindowSizeMsg, width=0)
		// permanently closed the sidebar for the session.
		if sidebarW < m.theme.Layout.SidebarMinWidth {
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
	// Reserve: textarea (inputH) + border(2) + inline status row
	// inside the bordered box(1) + outer status row(1) = inputH+4.
	// The old constant was 3, which left the chat area 1 row too
	// tall and pushed content over the pane edge so the first chat
	// block got clipped off the top.
	mainH := m.height - inputH - 4
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
	// Empty-state: draw the banner directly into the left column
	// (bypassing the viewport) so the top of the logo isn't eaten by
	// the viewport's scroll position when content is taller than the
	// pane. Don't pad to mainH — vp.View() on empty content returns
	// an empty string, so the input box normally floats up; we match
	// that behaviour and let the banner occupy only its own rows.
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

	// File-picker popover (triggered by `@` in the buffer). Rendered
	// INSIDE the bordered input frame, above the textarea, so the
	// suggestion column stays visually anchored to the input cursor
	// instead of floating at the top of the screen.
	var pickerPrefix string
	if m.filePicker.Visible && len(m.filePicker.Matches) > 0 {
		// Leave 2 cols of breathing room inside the border + padding.
		pickerPrefix = m.filePicker.View(mainW-4) + "\n"
	}

	body := pickerPrefix + m.input.View() + "\n" + strings.TrimRight(inline, "\n")

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

	// Cache-hit ratio: fraction of input tokens served from prompt
	// cache. Only meaningful on providers that report it
	// (Anthropic + cache-aware OAI-compat); elsewhere zero. Render
	// only when the ratio is non-trivial so it doesn't clutter.
	cacheRatio := ""
	if r := cacheHitRatio(m.usage); r > 0 {
		cacheRatio = fmt.Sprintf("cache %.0f%%", r*100)
	}

	// Queued-message indicator (mid-stream Enter buffer). Empty when
	// nothing queued — template conditional-renders the pill.
	queued := ""
	if m.queuedPrompt != "" {
		queued = trimSeed(m.queuedPrompt, 40)
	}

	body, err := m.renderer.Exec("status", map[string]any{
		"State":        state,
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Cwd":          m.cwd,
		"ErrorMessage": m.errorMsg,
		"Width":        width,
		"Tokens":       tokens,
		"Cost":         cost,
		"Cache":        cacheRatio,
		"Queued":       queued,
		"Budget":       m.budgetWarning(),
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

// installedAutoCompact returns the `auto-compact-<version>` directory
// name when a plugin matching that naming pattern is installed under
// $XDG_DATA_HOME/stado/plugins/, or "" otherwise. Used by the
// hard-threshold advisory to offer `/plugin:auto-compact-<ver> compact`
// as a one-click recovery when the plugin is available.
//
// Picks the lexicographically-latest version if multiple are
// installed — simple heuristic that matches install-order in
// practice (version bumps go forward).
func (m *Model) installedAutoCompact() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(cfg.StateDir(), "plugins"))
	if err != nil {
		return ""
	}
	latest := ""
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "auto-compact-") {
			continue
		}
		if e.Name() > latest {
			latest = e.Name()
		}
	}
	return latest
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
	// Session description — shown below the stado title so the user
	// knows which session they're in. Empty when unset, template
	// conditionally renders.
	sessionLabel := ""
	if m.session != nil {
		sessionLabel = runtime.ReadDescription(m.session.WorktreePath)
	}
	// Show just the basename of the loaded AGENTS.md / CLAUDE.md so
	// the user knows which file informed the system prompt, without
	// eating sidebar width with a full path.
	instructionsName := ""
	if m.systemPromptPath != "" {
		instructionsName = filepath.Base(m.systemPromptPath)
	}
	// Skills count — surfaces the feature's existence. Empty string
	// (instead of "0") when no skills are loaded so the template
	// conditional hides the row entirely, not showing "Skills: 0"
	// which would look broken.
	skillsCount := ""
	if n := len(m.skills); n > 0 {
		verb := "skills"
		if n == 1 {
			verb = "skill"
		}
		skillsCount = fmt.Sprintf("%d %s — /skill", n, verb)
	}
	data := map[string]any{
		"Title":            "stado",
		"Version":          "0.0.0-dev",
		"SessionLabel":     sessionLabel,
		"Model":            m.model,
		"ProviderName":     m.providerDisplayName(),
		"Cwd":              m.cwd,
		"TokensHuman":      fmt.Sprintf("%s tokens", humanize(m.usage.InputTokens+m.usage.OutputTokens)),
		"TokenPercent":     tokPct,
		"CostHuman":        fmt.Sprintf("$%.2f spent", m.usage.CostUSD),
		"Todos":            m.todos,
		"Width":            width - 4,
		"InstructionsName": instructionsName,
		"SkillsCount":      skillsCount,
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
	msg := agent.Text(agent.RoleUser, text)
	m.blocks = append(m.blocks, block{kind: "user", body: text})
	m.msgs = append(m.msgs, msg)
	m.persistMessage(msg)
}

func (m *Model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
}

// persistMessage append-writes msg to this session's conversation
// log so a future `stado` boot under the same worktree can resume
// the conversation. Best-effort: a disk error degrades resume but
// shouldn't interrupt the live session, so we swallow errors here
// (they already log through slog via OpenSession's OnCommit).
func (m *Model) persistMessage(msg agent.Message) {
	if m.session == nil {
		return
	}
	_ = runtime.AppendMessage(m.session.WorktreePath, msg)
}

// persistReplacement rewrites the conversation log with the current
// m.msgs state. Called after compaction-accept — the compacted form
// is what the user wants to resume, not the pre-compaction trail.
// Same best-effort error handling as persistMessage.
func (m *Model) persistReplacement() {
	if m.session == nil {
		return
	}
	_ = runtime.WriteConversation(m.session.WorktreePath, m.msgs)
}

// LoadPersistedConversation seeds m.msgs + m.blocks from whatever
// `runtime.LoadConversation` finds under the session's worktree. No-op
// when the conversation file is absent (fresh session) or the session
// itself is nil (test harness). Callers invoke this once at TUI boot,
// after the session is wired but before the first user input.
//
// Only text blocks are recreated faithfully. Tool-use / tool-result /
// thinking / image blocks are summarised with placeholder tags since
// the TUI's live-render pipeline for those is tied to in-flight
// streaming events that aren't present on replay. A future iteration
// could reconstruct them more fully; for now, the user sees enough to
// know what the conversation was without losing the m.msgs LLM-side
// prompt prefix.
func (m *Model) LoadPersistedConversation() {
	if m.session == nil {
		return
	}
	loaded, err := runtime.LoadConversation(m.session.WorktreePath)
	if err != nil || len(loaded) == 0 {
		return
	}
	m.msgs = loaded
	m.blocks = append(m.blocks, msgsToBlocks(loaded)...)
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("resumed session — %d prior message(s) loaded from disk.", len(loaded)),
	})
}

// msgsToBlocks renders a persisted message slice into the TUI's
// block model so the user sees the prior conversation on resume.
// Multi-block messages collapse into one per role; non-text blocks
// get short placeholder tags so the UI doesn't show blank
// assistant turns for tool-heavy history.
func msgsToBlocks(msgs []agent.Message) []block {
	out := make([]block, 0, len(msgs))
	for _, msg := range msgs {
		var body string
		for _, b := range msg.Content {
			switch {
			case b.Text != nil:
				if body != "" {
					body += "\n"
				}
				body += b.Text.Text
			case b.Thinking != nil:
				body += "[thinking]"
			case b.ToolUse != nil:
				body += "[tool_use " + b.ToolUse.Name + "]"
			case b.ToolResult != nil:
				body += "[tool_result]"
			case b.Image != nil:
				body += "[image]"
			}
		}
		kind := "assistant"
		switch msg.Role {
		case agent.RoleUser:
			kind = "user"
		case agent.RoleTool:
			kind = "tool"
		}
		out = append(out, block{kind: kind, body: body})
	}
	return out
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

	// Span ancestor is m.rootCtx (Background or a cross-process
	// traceparent-enriched context — see Phase 9.4/9.5), so turns
	// inside a forked session link back to the parent's trace tree.
	ctx, cancel := context.WithCancel(m.rootCtx)
	m.streamMu.Lock()
	m.streamCancel = cancel
	m.state = stateStreaming
	m.errorMsg = ""
	m.turnStart = time.Now()
	m.streamMu.Unlock()

	req := agent.TurnRequest{
		Model:    m.model,
		Messages: m.msgs,
		Tools:    m.toolDefs(),
		System:   m.systemPrompt,
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
		asstMsg := agent.Message{Role: agent.RoleAssistant, Content: asstBlocks}
		m.msgs = append(m.msgs, asstMsg)
		m.persistMessage(asstMsg)
	}

	if len(m.turnToolCalls) == 0 {
		m.state = stateIdle
		// Drain any queued follow-up message the user typed while the
		// previous turn was streaming. The block was already appended
		// at queue-time for immediate visual feedback; drain just
		// adds the message to m.msgs (the LLM-facing history) and
		// kicks the next turn. Slash commands route through
		// handleSlash. Queued prompts bypass the hard-threshold gate
		// on the theory that if the user decided to queue something
		// mid-stream, they can react to the block on arrival.
		if m.queuedPrompt != "" {
			queued := m.queuedPrompt
			m.queuedPrompt = ""
			if strings.HasPrefix(queued, "/") {
				return m.handleSlash(queued)
			}
			// Block was already appended at submit-time. Just thread
			// it through msgs + persistence so the next stream sees
			// it as a user turn.
			msg := agent.Text(agent.RoleUser, queued)
			m.msgs = append(m.msgs, msg)
			m.persistMessage(msg)
			return m.startStream()
		}
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

	// Session id (if we're in one) so users can copy-paste into
	// `stado session fork` / `session tree` without a separate /session
	// lookup. Zero-value session fields are tolerated — a TUI running
	// outside a session prints "(no session)".
	if m.session != nil && m.session.ID != "" {
		sb.WriteString(fmt.Sprintf("session: %s\n", m.session.ID))
	}

	// Cost / budget. Cost is always shown; budget caps only when set.
	sb.WriteString(fmt.Sprintf("cost: $%.4f\n", m.usage.CostUSD))
	if m.budgetWarnUSD > 0 || m.budgetHardUSD > 0 {
		w := "(unset)"
		if m.budgetWarnUSD > 0 {
			w = fmt.Sprintf("$%.2f", m.budgetWarnUSD)
		}
		h := "(unset)"
		if m.budgetHardUSD > 0 {
			h = fmt.Sprintf("$%.2f", m.budgetHardUSD)
		}
		sb.WriteString(fmt.Sprintf("budget: warn=%s · hard=%s", w, h))
		if m.budgetAcked {
			sb.WriteString(" · ack")
		}
		sb.WriteString("\n")
	}

	// Project-level instructions (AGENTS.md / CLAUDE.md), if loaded.
	if m.systemPromptPath != "" {
		sb.WriteString(fmt.Sprintf("instructions: %s\n", filepath.Base(m.systemPromptPath)))
	}
	// Loaded skills.
	if len(m.skills) > 0 {
		names := make([]string, 0, len(m.skills))
		for _, s := range m.skills {
			names = append(names, s.Name)
		}
		sb.WriteString(fmt.Sprintf("skills: %d loaded — %s\n", len(names), strings.Join(names, ", ")))
	}
	// post_turn hook, if configured.
	if m.hookRunner.PostTurnCmd != "" {
		cmd := m.hookRunner.PostTurnCmd
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		sb.WriteString(fmt.Sprintf("hook post_turn: %s\n", cmd))
	}

	sb.WriteString("options: /compact (summarise + confirm)  ·  /retry (regenerate last turn)  ·  session tree / session fork --at turns/<N>")
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

	// Parent-link through rootCtx so the compaction turn's spans
	// thread into the session's trace tree (Phase 9.4/9.5).
	ctx, cancel := context.WithCancel(m.rootCtx)
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
		// Rewrite the on-disk conversation log to match the compacted
		// state so a future resume sees the summary instead of the
		// full pre-compaction trail. Dual-ref commit (tree + trace)
		// preserves the raw turns separately on the trace ref for
		// audit, so nothing is lost.
		m.persistReplacement()

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
	// /skill lists loaded skills; /skill:<name> injects the body as
	// a user message so the LLM acts on it on the next turn.
	if parts[0] == "/skill" || strings.HasPrefix(parts[0], "/skill:") {
		return m.handleSkillSlash(parts)
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
	case "/sessions":
		m.appendBlock(block{kind: "system", body: m.renderSessionsOverview()})
	case "/describe":
		m.handleDescribeSlash(parts)
	case "/budget":
		m.handleBudgetSlash(parts)
	case "/retry":
		return m.handleRetrySlash()
	case "/session":
		m.handleSessionSlash()
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

// bannerFor returns the startup banner suitable for the given
// chat-column width. Width under 90 cols returns "" so the banner
// isn't truncated mid-line — narrow terminals just see the plain
// empty chat area.
//
// Plain variant is used here: bubbletea's viewport counts ANSI
// escape bytes as content, so the 256-colour banner (~50KB of
// escapes) confuses wrap-measurement. The plain banner is still
// recognisable as the stado logo and fits cleanly.
func bannerFor(vpWidth int) string {
	if vpWidth < 90 {
		return ""
	}
	return banner.Plain()
}

// renderBannerBlock returns the banner trimmed to at most maxH rows
// so a short terminal gets a truncated banner rather than one that
// overflows and pushes the input box off-screen. No bottom padding:
// vp.View() on empty content returns an empty string (not maxH
// blanks), so the input box floats up naturally — we mirror that
// so the banner occupies just its own rows.
func renderBannerBlock(width, maxH int) string {
	raw := bannerFor(width)
	if raw == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) > maxH {
		lines = lines[:maxH]
	}
	return strings.Join(lines, "\n")
}

// firePostTurnHook invokes the user-configured post_turn shell
// command (if any) with a JSON payload on stdin. No-op when the
// hook isn't configured. Errors / timeouts are logged by the hook
// runner; never propagated — the turn is over.
func (m *Model) firePostTurnHook() {
	if m.hookRunner.PostTurnCmd == "" {
		return
	}
	excerpt := m.turnText
	if len(excerpt) > 200 {
		excerpt = excerpt[:200]
	}
	dur := int64(0)
	if !m.turnStart.IsZero() {
		dur = time.Since(m.turnStart).Milliseconds()
	}
	m.hookRunner.FirePostTurn(m.rootCtx, hooks.PostTurnPayload{
		Event:       "post_turn",
		TurnIndex:   len(m.msgs),
		TokensIn:    m.usage.InputTokens,
		TokensOut:   m.usage.OutputTokens,
		CostUSD:     m.usage.CostUSD,
		TextExcerpt: excerpt,
		DurationMS:  dur,
	})
}

// maybeEmitBudgetWarning fires a one-time system block once cumulative
// cost crosses the warn cap, so users don't keep seeing the same
// notice every turn. Called from handleStreamEvent on every Usage
// update.
func (m *Model) maybeEmitBudgetWarning() {
	if m.budgetWarnUSD <= 0 || m.budgetWarned {
		return
	}
	if m.usage.CostUSD < m.budgetWarnUSD {
		return
	}
	m.budgetWarned = true
	cap := m.budgetWarnUSD
	hint := ""
	if m.budgetHardUSD > 0 {
		hint = fmt.Sprintf(" — hard cap at $%.2f", m.budgetHardUSD)
	}
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("budget warning: cost $%.2f crossed warn cap $%.2f%s", m.usage.CostUSD, cap, hint),
	})
	m.renderBlocks()
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

	return runPluginToolAsync(pluginDir, mf, *tdef, argsJSON, nameVer, m.buildPluginBridge(mf.Name))
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

// activeAtTrigger returns (atPos, query, ok) when the input cursor
// sits inside an @-prefixed word. atPos is the byte index of '@' in
// the buffer; query is everything between '@' and the cursor. The
// trigger is only recognised when '@' is at the start of input or
// directly preceded by whitespace, so email addresses and package
// references don't accidentally fire the picker.
func (m *Model) activeAtTrigger() (atPos int, query string, ok bool) {
	val := m.input.Value()
	cursor := m.input.CursorOffset()
	if cursor < 0 || cursor > len(val) {
		return 0, "", false
	}
	for i := cursor - 1; i >= 0; i-- {
		r := val[i]
		if r == '@' {
			if i == 0 || val[i-1] == ' ' || val[i-1] == '\n' || val[i-1] == '\t' {
				return i, val[i+1 : cursor], true
			}
			return 0, "", false
		}
		if r == ' ' || r == '\n' || r == '\t' {
			return 0, "", false
		}
	}
	return 0, "", false
}

// updateFilePickerFromInput inspects the current editor state and
// shows/hides/refreshes the file picker accordingly. Called after
// every keypress the editor processes. No-op when the picker's
// visibility is already correct for the buffer state.
func (m *Model) updateFilePickerFromInput() {
	atPos, query, ok := m.activeAtTrigger()
	if !ok {
		if m.filePicker.Visible {
			m.filePicker.Close()
		}
		return
	}
	if !m.filePicker.Visible || m.filePicker.Anchor != atPos {
		cwd := m.cwd
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		m.filePicker.Open(cwd, atPos)
	}
	m.filePicker.SetQuery(query)
}

// acceptFilePickerSelection replaces the @<query> fragment in the
// input buffer with the highlighted path, followed by a space so the
// user can keep typing. Closes the picker. No-op when nothing is
// selected — the caller falls through to the normal submit/tab path.
func (m *Model) acceptFilePickerSelection() {
	sel := m.filePicker.Selected()
	if sel == "" {
		return
	}
	val := m.input.Value()
	anchor := m.filePicker.Anchor
	cursor := m.input.CursorOffset()
	if anchor < 0 || anchor > len(val) || cursor < anchor || cursor > len(val) {
		m.filePicker.Close()
		return
	}
	newVal := val[:anchor] + sel + " " + val[cursor:]
	m.input.SetValue(newVal)
	m.filePicker.Close()
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

func trimSeed(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// runPluginToolAsync spawns a fresh wazero runtime, instantiates the
// module under its capability-bound Host, invokes the tool, and posts
// the outcome back via pluginRunResultMsg. Hard-capped at 30s so a
// runaway plugin can't wedge the TUI.
func runPluginToolAsync(dir string, mf *plugins.Manifest, tdef plugins.ToolDef, argsJSON, pluginID string, bridge *pluginRuntime.SessionBridgeImpl) tea.Cmd {
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

// cacheHitRatio returns fraction of prompt tokens served from prompt
// cache on the last turn. Formula: CacheReadTokens /
// (CacheReadTokens + InputTokens) — the numerator is what the cache
// saved the user; the denominator is everything the model had to
// "look at" whether from cache or from the fresh prompt body. Returns
// 0 when either the provider doesn't report cache tokens or there
// were no prompts yet.
func cacheHitRatio(u agent.Usage) float64 {
	total := u.CacheReadTokens + u.InputTokens
	if total == 0 || u.CacheReadTokens == 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(total)
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
