package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/skills"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/agentpicker"
	"github.com/foobarto/stado/internal/tui/filepicker"
	"github.com/foobarto/stado/internal/tui/input"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/modelpicker"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/sessionpicker"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/tui/themepicker"
	"github.com/foobarto/stado/pkg/agent"
)

// block is the UI-level conversation unit. One conversation is a slice of these.
type block struct {
	kind    string // "user" | "assistant" | "thinking" | "tool" | "system" | "btw"
	body    string
	meta    string
	details string

	// queued: user message appended to the chat while a turn was in
	// flight. The block renders with a muted "queued" tag until the
	// current stream drains and this message is dispatched to the
	// model. Lets users see their own follow-up lined up instead of
	// wondering if it registered.
	queued bool

	// Tool-call specific
	toolID     string
	toolName   string
	toolArgs   string
	toolResult string
	startedAt  time.Time
	endedAt    time.Time

	// expanded toggles tool call bodies and assistant turn details.
	expanded bool

	// Render cache: avoid re-running glamour/markdown on every frame.
	// Streaming a long assistant message causes renderBlocks to fire
	// 10+ times/sec; without caching, each tick re-renders every past
	// block from scratch — glamour alone costs ~10ms per 3KB block, so
	// for long conversations the main goroutine blocks for hundreds of
	// ms per tick and the UI stops responding to keys. We cache per-
	// block and invalidate on (body | width | expanded) change.
	cachedWidth        int
	cachedOut          string
	cachedMeta         string
	cachedDetails      string
	cachedExpand       bool
	cachedResult       string
	cachedThinkingMode thinkingDisplayMode
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
	// stateQuitConfirm: user pressed ctrl+d — show a confirmation
	// popup so they don't accidentally exit mid-session.
	stateQuitConfirm
)

// inputMode switches the agent between read-only analysis ("Plan"),
// full execution ("Do"), and off-band queries ("BTW").
type inputMode int

const (
	modeDo inputMode = iota
	modePlan
	modeBTW
)

func (m inputMode) String() string {
	switch m {
	case modePlan:
		return "Plan"
	case modeBTW:
		return "BTW"
	default:
		return "Do"
	}
}

type thinkingDisplayMode int

const (
	thinkingShow thinkingDisplayMode = iota
	thinkingTail
	thinkingHide
)

// Internal messages used by the bubbletea update loop.
type (
	streamEventMsg        struct{ ev agent.Event }
	streamBatchMsg        struct{ evs []agent.Event }
	streamErrorMsg        struct{ err error }
	logTailMsg            struct{ line string }
	localFallbackReadyMsg struct {
		provider     agent.Provider
		providerName string
		models       []string
	}
	streamTickMsg            struct{}
	streamDoneMsg            struct{}
	recoveryTimeoutMsg       struct{}
	toolsExecutedMsg         struct{ results []agent.ToolResultBlock }
	pluginApprovalRequestMsg struct {
		title    string
		body     string
		response chan bool
	}
	pluginApprovalCancelMsg struct {
		response chan bool
	}
	// pluginRunResultMsg carries the outcome of a `/plugin:...` invocation
	// back to the Update loop. Rendered as a system block so the user
	// sees the tool's return value alongside the conversation flow.
	pluginRunResultMsg struct {
		plugin  string
		tool    string
		content string
		errMsg  string
	}
	// toolResultMsg carries the outcome of a single approved or
	// remembered-allow tool call back to the Update loop. The UI stays
	// responsive while the tool runs; this message triggers the next
	// remembered tool or the final toolsExecutedMsg drain.
	toolResultMsg struct {
		result agent.ToolResultBlock
	}
	// toolTickMsg ticks every 250ms while a tool is running so the
	// elapsed-time counter in the tool block re-renders live.
	toolTickMsg struct{}
	// pluginForkMsg is dispatched when a plugin's ForkFn closure
	// creates a child session. Surfaced in Update as a user-visible
	// system block per DESIGN invariant 4 — "user-visible by default."
	pluginForkMsg struct {
		plugin    string // plugin name from the manifest
		childID   string // new session ID
		atTurnRef string // fork point, or empty for parent tree HEAD
		seed      string // plugin-provided seed / summary text
	}
	// btwResultMsg carries the reply from an async BTW query back to
	// the Update loop.  Rendered as a "btw" block so the user sees
	// the answer alongside the conversation, but it is NOT appended
	// to the conversation history the main thread uses.
	btwResultMsg struct {
		question string
		reply    string
		errMsg   string
	}
)

// Model is the root bubbletea model for stado's TUI.
type Model struct {
	// Config + infrastructure
	cwd      string
	cfg      *config.Config
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
	backgroundPlugins     []*pluginRuntime.BackgroundPlugin
	backgroundTickRunning bool
	backgroundTickQueued  bool
	backgroundTickPayload []byte
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
	// providerProbePending is true while the async startup probe for an
	// implicit local fallback is still running. First-submit uses this to
	// queue the prompt instead of blocking the UI on a duplicate probe.
	providerProbePending bool

	// Tool execution + git state. executor may be nil (no session) in which
	// case tool calls are reported but not executed.
	executor *tools.Executor
	session  *stadogit.Session
	// Cached footer VCS summary. Status rendering happens frequently, so
	// avoid probing git on every frame.
	statusGitCwd       string
	statusGitSummary   string
	statusGitCheckedAt time.Time
	// sessionUIStates keeps lightweight view state for inactive sessions
	// inside this TUI process.
	sessionUIStates map[string]sessionUIState

	// UI components
	input       *input.Editor
	slash       *palette.Model
	slashInline bool
	agentPick   *agentpicker.Model
	modelPicker *modelpicker.Model
	sessionPick *sessionpicker.Model
	themePick   *themepicker.Model
	filePicker  *filepicker.Model
	vp          viewport.Model
	showHelp    bool
	showStatus  bool

	// mode is Do (default — all tools allowed) or Plan (mutating + exec
	// tools hidden from the model so it produces an analysis-only
	// response). Tab toggles.
	mode inputMode
	// thinkingMode controls how provider-native thinking blocks are
	// rendered in the TUI. It never changes what is captured or
	// persisted; it is display-only.
	thinkingMode thinkingDisplayMode

	// Conversation state
	blocks    []block
	msgs      []agent.Message
	todos     []todo
	subagents []subagentActivity

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
	// recoveryPrompt is the blocked prompt currently waiting for a
	// plugin-driven context recovery fork. When the expected plugin
	// creates a child session, the TUI switches to it and replays this
	// prompt there instead of dropping it.
	recoveryPrompt       string
	recoveryPluginName   string
	recoveryPluginActive bool

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
	width       int
	height      int
	sidebarOpen bool
	// sidebarDebug expands the right sidebar with operational details
	// such as log tail, unknown context-provider state, and sandbox
	// posture. Default false keeps the normal sidebar quiet.
	sidebarDebug bool
	// sidebarWidth is the user's preferred sidebar width for this TUI
	// session. Zero means "use the theme default". The rendered width
	// is still clamped per-frame to fit the current terminal.
	sidebarWidth int

	// logTail holds a short in-process tail of slog lines captured
	// while the TUI is active. It is shown in the sidebar so runtime
	// / plugin diagnostics stop trampling the terminal surface.
	logMu   sync.Mutex
	logTail []string

	// Approval
	approval              *approvalRequest
	approvalFocused       bool
	approvalAllowSelected bool

	// Back-channel for events from the provider goroutine.
	program *tea.Program

	// toolCancel cancels an in-flight async tool call so the user can
	// stop it mid-execution after approving it. Only non-nil when a
	// tool is actively running; reset in toolResultMsg or streamCancel.
	toolCancel context.CancelFunc
	toolMu     sync.Mutex
	// toolTickTimer is the handle for the live-elapsed update while a
	// tool runs. Cancelled when the toolResultMsg arrives.
	toolTickTimer *time.Timer

	// Per-turn accumulators (reset on startStream).
	turnText      string
	turnThinking  string
	turnThinkSig  string
	turnToolCalls []agent.ToolUseBlock
	turnAllowed   map[string]struct{}
	turnMode      inputMode
	turnModel     string
	turnProvider  string

	// Tool queue: calls waiting for execution + the results already
	// collected during this tool batch. When the queue drains we emit
	// a toolsExecutedMsg and the agent loop continues.
	pendingCalls   []agent.ToolUseBlock
	pendingResults []agent.ToolResultBlock

	// systemPrompt is the project-root AGENTS.md / CLAUDE.md body
	// resolved at TUI startup and passed into the system-prompt
	// template as ProjectInstructions. Empty if no file was found
	// walking up from cwd.
	systemPrompt     string
	systemPromptPath string
	// systemPromptTemplate is loaded from ~/.config/stado/system-prompt.md
	// and executed with runtime metadata for every provider request.
	systemPromptTemplate string

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
	turnStart        time.Time
	lastStreamRender time.Time

	// splitView toggles a two-pane chat area: activity blocks (tool
	// + system) on top + conversation blocks (user/assistant/thinking)
	// on the bottom. Toggled with /split.
	splitView  bool
	activityVP viewport.Model

	// streamBuf decouples the stream goroutine from bubbletea's
	// unbuffered program channel. The goroutine appends events here
	// under the mutex; a tea.Tick-driven drain (streamTickMsg) runs
	// on the main loop, forwards the batch, and reschedules itself
	// while the stream is live. Prevents high-rate reasoning
	// deltas from starving the KeyMsg path on the same channel.
	streamBuf       []agent.Event
	streamBufMu     sync.Mutex
	streamBufClosed bool
}

type approvalRequest struct {
	title    string
	body     string
	response chan bool
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
		agentPick:        agentpicker.New(),
		modelPicker:      modelpicker.New(),
		sessionPick:      sessionpicker.New(),
		themePick:        themepicker.New(),
		filePicker:       filepicker.New(),
		sessionUIStates:  make(map[string]sessionUIState),
		vp:               viewport.New(0, 0),
		activityVP:       viewport.New(0, 0),
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

// SetApprovals is kept for config compatibility, but tool-call approvals
// are no longer enforced by the TUI. Plugins may request approval
// explicitly through the plugin host when they declare the UI capability.
func (m *Model) SetApprovals(_ string, _ []string) {}

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
	done := tuiTraceCall("tui.ensureProvider",
		"has_provider", m.provider != nil,
		"provider_name", m.providerName,
		"probe_pending", m.providerProbePending)
	defer done("state", int(m.state))
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
	tuiTrace("provider builder start", "provider_name", m.providerName)
	p, err := m.buildProvider()
	if err != nil {
		tuiTrace("provider builder error", "provider_name", m.providerName, "error", err.Error())
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
	tuiTrace("provider ready", "provider_name", m.provider.Name())
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
		models := r.RunnableModels()
		switch {
		case r.LoadStateKnown && len(models) == 0:
			fmt.Fprintf(&b, "  %-9s %s  (%d installed, none loaded)\n", r.Name, r.Endpoint, len(r.Models))
		case len(models) == 0:
			fmt.Fprintf(&b, "  %-9s %s  (no models loaded)\n", r.Name, r.Endpoint)
		case len(models) == 1:
			fmt.Fprintf(&b, "  %-9s %s  (1 model: %s)\n", r.Name, r.Endpoint, models[0])
		default:
			fmt.Fprintf(&b, "  %-9s %s  (%d models, e.g. %s)\n",
				r.Name, r.Endpoint, len(models), models[0])
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
	if env := config.ProviderAPIKeyEnv(p); env != "" {
		return env
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
