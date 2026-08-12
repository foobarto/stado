package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

const appName = "stado"
const maxConfigBytes int64 = 1 << 20
const maxSystemPromptTemplateBytes int64 = 1 << 20

// Config is the top-level stado configuration.
//
// The schema covers provider selection, sandboxing, git/audit behavior,
// telemetry, interop, plugins, memory/context, agent/session behavior, the TUI,
// tool policy, budgets, hooks, supervision, harness mode, LSP, and completion
// verification. Security-sensitive sections are filtered when a project-local
// overlay is merged; see project_config.go and docs/commands/config.md.
type Config struct {
	ConfigPath string `koanf:"-"`
	// projectStadoDir is the absolute path of the .stado/ directory
	// found by walking up from cwd. Empty when no .stado/ exists.
	// EP-0035.
	projectStadoDir string

	Defaults  Defaults  `koanf:"defaults"`
	Approvals Approvals `koanf:"approvals"`
	MCP       MCP       `koanf:"mcp"`

	Inference  Inference  `koanf:"inference"`
	Sandbox    Sandbox    `koanf:"sandbox"`
	Git        Git        `koanf:"git"`
	OTel       OTel       `koanf:"otel"`
	ACP        ACP        `koanf:"acp"`
	Plugins    Plugins    `koanf:"plugins"`
	Memory     Memory     `koanf:"memory"`
	Context    Context    `koanf:"context"`
	Agent      Agent      `koanf:"agent"`
	Sampling   Sampling   `koanf:"sampling"`
	Sessions   Sessions   `koanf:"sessions"`
	TUI        TUI        `koanf:"tui"`
	Keymap     Keymap     `koanf:"keymap"`
	Tools      Tools      `koanf:"tools"`
	Aliases    Aliases    `koanf:"aliases"`
	Budget     Budget     `koanf:"budget"`
	Hooks      Hooks      `koanf:"hooks"`
	Runtime    Runtime    `koanf:"runtime"`
	Supervisor Supervisor `koanf:"supervisor"`
	Harness    Harness    `koanf:"harness"`
	LSP        LSP        `koanf:"lsp"`
	Verify     Verify     `koanf:"verify"`
}

// Verify configures the opt-in command gate at the agent loop's natural exit.
// Commands are operator-authored and always execute through the session tool
// sandbox, never a raw host shell.
type Verify struct {
	Commands  []string `koanf:"commands"`
	MaxRounds int      `koanf:"max_rounds"`
	Strict    bool     `koanf:"strict"`
}

// LSP is the [lsp] config section.
type LSP struct {
	// AutoDiagnostics enables the TUI hook that, after a mutating edit to a
	// source file, launches the matching language server and surfaces its
	// diagnostics. Default false (Codex #12): the server is an operator-PATH
	// binary that reads repo-controlled project config (tsconfig/pyproject/
	// rust-analyzer settings) and may invoke build tooling, and today it runs
	// unsandboxed with no per-edit consent — auto-spawning it on every edit in
	// an untrusted repo crosses the repo→host boundary. Opt in explicitly:
	//
	//	[lsp]
	//	auto_diagnostics = true
	AutoDiagnostics bool `koanf:"auto_diagnostics"`
}

// Hooks is the [hooks] config section — user-provided shell commands
// fired at completed turn boundaries across TUI, `stado run`, and
// headless `session.prompt`. MVP scope: notification-only.
// Commands can't block the turn or mutate state. stdout/stderr are
// logged to stado's stderr, not the TUI chat window, so a noisy hook
// doesn't eat the user's context.
//
//	[hooks]
//	post_turn = "notify-send 'stado' 'turn complete'"
//
// Each hook runs with /bin/sh -c so users can pipe, redirect, etc.
// A JSON payload with the turn's usage numbers is piped to stdin so
// scripts can act on token counts / cost without parsing a log file:
//
//	{"event":"post_turn", "turn_index":N, "tokens_in":X,
//	 "tokens_out":Y, "cost_usd":Z, "text_excerpt":"..."}
//
// Hook execution has a 5-second wall-clock timeout; longer-running
// work should fork + exit. Exit codes are recorded but not acted on.
type Hooks struct {
	// PostTurn fires after every completed turn on the supported
	// interactive and non-interactive surfaces.
	// Empty = no hook.
	PostTurn string `koanf:"post_turn"`

	// Lifecycle is the ordered set of scriptable deny/mutate hooks (F1).
	// Unlike PostTurn (a fire-and-forget shell notification), lifecycle
	// hooks run a Lua policy at pre/post-tool, pre/post-llm, and post-turn
	// interception points and can DENY (veto) or MUTATE (rewrite args /
	// llm requests / outputs). They run serially in config order, fail
	// open on error, with a 5s per-hook timeout.
	//
	//	[[hooks.lifecycle]]
	//	name = "deny-rm-rf"
	//	lua  = """
	//	  function pre_tool(p)
	//	    if p.tool == "shell__bash" and string.find(p.args, "rm %-rf") then
	//	      return { deny = "rm -rf blocked by policy" }
	//	    end
	//	  end
	//	"""
	//
	// SECURITY: like PostTurn, the entire [hooks] table is stripped from
	// project (.stado/config.toml) config — Lua is a code-execution
	// vector and must not be dictated by an untrusted repo. Lifecycle
	// hooks only take effect from user/global config.
	Lifecycle []LifecycleHook `koanf:"lifecycle"`

	// FailClosed flips the lifecycle runner's error posture. By default
	// (false) execution is FAIL-OPEN: a hook that errors, times out, or
	// panics is logged and treated as Continue — a broken policy hook must
	// not wedge the agent loop. When set to true, the same fault is treated
	// as a DENY at PRE points (the action is vetoed) and a deny-style
	// replacement at POST points, so a policy that *must* run becomes a
	// hard gate: if the gate can't be evaluated, the action doesn't happen.
	// Use this when a hook enforces a security boundary that a silent
	// fail-open would breach.
	//
	//	[hooks]
	//	fail_closed = true
	FailClosed bool `koanf:"fail_closed"`
}

// LifecycleHook is one scriptable deny/mutate hook. Exactly one of Lua
// (inline source) or LuaFile (path to a .lua file) must be set; Lua wins
// if both are. The Lua chunk defines global functions named after the
// lifecycle points it handles: pre_tool, post_tool, pre_llm, post_llm,
// post_turn.
type LifecycleHook struct {
	// Name is a short identifier surfaced in hook-failure logs.
	Name string `koanf:"name"`
	// Lua is inline Lua source for the hook body.
	Lua string `koanf:"lua"`
	// LuaFile is a path to a .lua file read at startup. Ignored when Lua
	// is set.
	LuaFile string `koanf:"lua_file"`
}

// Sessions configures session lifecycle policy. EP-0037 §C / NOTES §8.
//
// AutoPruneAfter is the duration after which completed sessions are
// pruned by stado on startup ("90d", "30d", or "" for never; default
// is "" — sessions are durable audit records by design). The
// duration is parsed at startup with time.ParseDuration extended for
// the "d" suffix.
//
// Auto-prune execution is not yet wired (TODO: connect to the
// existing `stado session prune` codepath at startup). This struct
// commits to the schema today; setting AutoPruneAfter has no
// effect until the startup hook lands.
type Sessions struct {
	AutoPruneAfter string `koanf:"auto_prune_after"`
}

// Budget is the [budget] config section — per-session guardrails on
// cost (USD) and/or token usage. Stado already tracks both on every
// provider turn; this adds thresholds to surface a warning and
// (optionally) hard-block new turns. All fields default to 0, meaning
// "no limit" — the guardrails are opt-in so cost-insensitive
// local-runner users don't see pills for nothing.
//
// Cost (USD) guards apply when the provider reports a per-turn cost
// (Anthropic, OpenAI, Google, paid OAI-compat presets). Token guards
// apply universally, including local runners where USD is always 0;
// useful when running on Ollama / LM Studio / vLLM where the meaningful
// budget is throughput, not dollars.
//
//	[budget]
//	warn_usd           = 1.00    # status-bar pill + one-time system block when crossed
//	hard_usd           = 5.00    # block further turns pending user ack
//	warn_tokens        = 100000  # combined input+output cumulative cap (warn)
//	hard_tokens        = 500000  # combined input+output cumulative cap (hard)
//	warn_input_tokens  = 0       # power-user: separate input-only cap (warn)
//	hard_input_tokens  = 0       # ... (hard)
//	warn_output_tokens = 0       # power-user: separate output-only cap (warn)
//	hard_output_tokens = 0       # ... (hard)
//
// Fractional dollars allowed; tokens are integers. Every cap is
// independent and any one firing aborts the loop / triggers the gate.
// Most users want the combined `*_tokens` (covers context-window
// growth + generation length together); the per-direction caps are
// for power users who want to bound output length without capping
// how much input context the model gets, or vice versa. Output
// tokens are 3–5× more expensive than input on most paid providers,
// so an output-only cap is the cheap-ish way to constrain spend
// without restricting context.
//
// A hard threshold below its corresponding warn threshold is a
// config error — the guard would never warn before blocking — and
// is ignored with a stderr warning at config-load time.
type Budget struct {
	WarnUSD          float64 `koanf:"warn_usd"`
	HardUSD          float64 `koanf:"hard_usd"`
	WarnTokens       int     `koanf:"warn_tokens"`
	HardTokens       int     `koanf:"hard_tokens"`
	WarnInputTokens  int     `koanf:"warn_input_tokens"`
	HardInputTokens  int     `koanf:"hard_input_tokens"`
	WarnOutputTokens int     `koanf:"warn_output_tokens"`
	HardOutputTokens int     `koanf:"hard_output_tokens"`
}

type Memory struct {
	// Enabled injects approved, scoped, non-secret memory snippets into
	// provider system prompts. On by default; set `enabled = false` under
	// [memory] to opt out (the unset-vs-explicit-false distinction is
	// resolved in Load via k.Exists). Only user-approved memories are ever
	// injected, so the default-on surface is reviewed context, not silent
	// capture.
	Enabled bool `koanf:"enabled"`
	// MaxItems caps prompt snippets retrieved per turn.
	MaxItems int `koanf:"max_items"`
	// BudgetTokens caps rough prompt-token spend for retrieved memories.
	BudgetTokens int `koanf:"budget_tokens"`
}

func (m Memory) EffectiveMaxItems() int {
	if m.MaxItems <= 0 {
		return 8
	}
	return m.MaxItems
}

func (m Memory) EffectiveBudgetTokens() int {
	if m.BudgetTokens <= 0 {
		return 800
	}
	return m.BudgetTokens
}

// Tools is the [tools] config section — user-level control over
// which bundled tools are visible to the agent. All tools are
// available by default. Either list is accepted; Enabled wins when
// both are set (it's an explicit allowlist so mentioning Disabled
// alongside is redundant).
//
//	[tools]
//	enabled  = ["read", "grep", "bash"]    # only these — allowlist mode
//	disabled = ["webfetch"]                 # remove specific tools from default set
//
// Tool names match the `Name()` each bundled tool returns (see
// internal/runtime/runtime.go BuildDefaultRegistry for the
// canonical list). Unknown names in either list are ignored with a
// warning on stderr — tolerates typos without refusing to boot.
type Tools struct {
	Enabled  []string `koanf:"enabled"`
	Disabled []string `koanf:"disabled"`
	// Autoload is the subset of enabled tools whose schemas are sent to
	// the model at every turn (EP-0037 §E). Tools not in this list are
	// still reachable via tools.search + tools.describe. Empty = use the
	// hardcoded default core (fs.*, shell.exec bare-name equivalents).
	Autoload []string `koanf:"autoload"`
	// AutoloadCategories adds every tool whose categories metadata
	// overlaps with one of these category names to the per-turn
	// autoload set. Layered ON TOP of Autoload — the union is what
	// the model sees each turn. Tester #7: lets HTB-tooling sessions
	// run lean and pull, e.g., `recon` tools always while `exploit`
	// tools stay lazy-loaded behind tools.activate.
	AutoloadCategories []string `koanf:"autoload_categories"`
	// Overrides maps a registry tool name to an installed plugin ID
	// (`<name>-<version>` or `<name>@<version>`). When set, the plugin's
	// matching tool declaration replaces the native/MCP tool under the
	// same registry name.
	Overrides map[string]string `koanf:"overrides"`
}

// Agent is the [agent] config section — capability-driven knobs that
// shape how the runtime talks to a given provider. Defaults land in
// Load() when unset.
type Agent struct {
	// Thinking controls extended-thinking behaviour:
	//   "auto" (default) — enable when the provider's Capabilities
	//                       report SupportsThinking=true
	//   "on"              — always enable, even if the provider will
	//                       reject (useful for debugging)
	//   "off"             — never enable
	Thinking string `koanf:"thinking"`
	// ThinkingBudgetTokens is the budget passed to providers that
	// accept one (Anthropic). Ignored when Thinking resolves to off.
	ThinkingBudgetTokens int `koanf:"thinking_budget_tokens"`
	// SystemPromptPath points at the editable Go template used to build
	// every provider system prompt. Empty means
	// ~/.config/stado/system-prompt.md, created on first config load.
	SystemPromptPath string `koanf:"system_prompt_path"`
	// SystemPromptTemplate is loaded from SystemPromptPath after config +
	// env resolution. It is intentionally not mapped back into koanf.
	SystemPromptTemplate string `koanf:"-" json:"-"`
}

// Sampling controls LLM sampling parameters injected into every
// TurnRequest. All fields default to the provider's own default when
// zero/nil — setting them here overrides the provider default globally.
// Use --temperature / --top-p / --top-k on `stado run` for one-shot
// overrides. EP-0036.
//
//	[sampling]
//	temperature = 0.7
//	top_p       = 0.9
//	top_k       = 40
type Sampling struct {
	Temperature *float64 `koanf:"temperature"`
	TopP        *float64 `koanf:"top_p"`
	TopK        *int     `koanf:"top_k"`
}

// TUI contains display-only preferences for the interactive terminal UI.
// These settings do not change provider requests or persisted transcripts.
type TUI struct {
	// Theme optionally pins a bundled theme id such as stado-dark,
	// stado-light, stado-contrast, or stado-rose.
	Theme string `koanf:"theme"`
	// ThinkingDisplay controls how provider-native thinking blocks are
	// rendered in the viewport: preview (clip to a few lines, the
	// default), auto (full while streaming, one line once done),
	// collapsed (always one line), or expanded (always full). Legacy
	// values show/tail/hide still load (mapped to expanded/preview/
	// collapsed).
	ThinkingDisplay string `koanf:"thinking_display"`
	// MouseCapture toggles app-level mouse handling. When true (default),
	// stado captures mouse events for click-to-expand on tool blocks +
	// scroll-wheel. The trade-off is that the terminal's native
	// click-drag-to-select-text is suppressed; users can still hold
	// Shift while dragging on most modern terminals to bypass capture.
	// When false, mouse capture is fully off — native selection works
	// everywhere but click-to-expand and mouse scroll are unavailable
	// (use alt+up/alt+down to navigate tool blocks instead).
	MouseCapture *bool `koanf:"mouse_capture"`
	// SidebarWidth persists the operator's preferred sidebar width across
	// sessions. Adjusted in the TUI with `ctrl+x [` (wider) and
	// `ctrl+x ]` (narrower). 0 = unset (use the layout default).
	SidebarWidth int `koanf:"sidebar_width"`
	// Sidebar configures which sidebar sections show and in what order
	// (#21). Empty/absent = the default layout. Entries are built-in
	// section ids (header, now, subagents, risk, agent, repo, logs, todos).
	// Plugin-contributed panel ids become valid once the plugin display API
	// lands (part 2).
	Sidebar TUISidebar `koanf:"sidebar"`
	// Footer configures which footer segments are VISIBLE (#21). Order is
	// fixed by the template — listing a segment shows it, omitting it hides
	// it. Empty/absent = the default segments.
	Footer TUIFooter `koanf:"footer"`
	// ToolOutputCollapsedHeight caps how many rows a collapsed tool block's
	// streaming output occupies in the viewport. Long outputs (test logs,
	// big greps, build output) are clipped to this height with a "… N more
	// lines (shift+tab)" footer until the block is expanded (shift+tab or a
	// mouse click). 0 = unset (uses the default). The effective value is
	// clamped to [3, 20] by EffectiveToolOutputCollapsedHeight.
	ToolOutputCollapsedHeight int `koanf:"tool_output_collapsed_height"`
	// ToolDisplay controls how tool-output panes are rendered, using the
	// same vocabulary as ThinkingDisplay: preview (clip to
	// tool_output_collapsed_height rows, the default), auto (full while
	// the tool runs, one line once the result arrives), collapsed (always
	// one line), or expanded (always full).
	ToolDisplay string `koanf:"tool_display"`
}

// EffectiveToolOutputCollapsedHeight returns the row budget for a
// collapsed tool block's output: the configured value clamped to
// [3, 20], defaulting to 8 when unset (0) or out of range below the
// minimum. Mirrors Memory.EffectiveMaxItems' unset-means-default shape.
func (t TUI) EffectiveToolOutputCollapsedHeight() int {
	const (
		def = 8
		min = 3
		max = 20
	)
	h := t.ToolOutputCollapsedHeight
	if h <= 0 {
		return def
	}
	if h < min {
		return min
	}
	if h > max {
		return max
	}
	return h
}

// TUISidebar is the [tui.sidebar] block — an ordered list of section ids.
type TUISidebar struct {
	Sections []string `koanf:"sections"`
}

// TUIFooter is the [tui.footer] block — the set of visible segment ids
// (visibility only; the footer's order is fixed by the template).
type TUIFooter struct {
	Segments []string `koanf:"segments"`
}

// Keymap is the [keymap] section — selects a named keybinding schema and
// applies per-action overrides on top of it. Schema picks the base layout
// (emacs, vscode, or the modal "vim"); empty is treated as the emacs default.
// Bindings maps an action name (e.g. "sidebar_toggle") to a comma-separated
// key list (e.g. "ctrl+y,ctrl+o") that REPLACES that action's schema binding.
//
//	[keymap]
//	schema = "vscode"
//	[keymap.bindings]
//	sidebar_toggle = "ctrl+y"
//	messages_first = "ctrl+home"
//
// Override application lives in internal/tui/keys.LoadOverrides; an unknown
// action name is reported as a non-fatal error (the TUI logs it to stderr and
// keeps booting) while the remaining valid overrides still apply.
type Keymap struct {
	// Schema names the base layout: "emacs" (default), "vscode", or "vim"
	// (modal). Empty is treated as emacs. The "vim" schema layers a modal
	// editing engine (NORMAL/INSERT/VISUAL) over the input; see
	// internal/tui/vimmode and docs/commands/tui.md.
	Schema string `koanf:"schema"`
	// Bindings overrides individual actions by name. Each value is a
	// comma-separated key list that replaces the action's schema binding.
	Bindings map[string]string `koanf:"bindings"`
}

// Context is Phase 11's [context] section: soft/hard percentage
// thresholds against the active model's MaxContextTokens. Defaults applied
// in Load when unset.
type Context struct {
	// SoftThreshold is the fraction of MaxContextTokens (0..1) at which
	// the TUI + headless surface a warning. Default 0.70.
	SoftThreshold float64 `koanf:"soft_threshold"`
	// HardThreshold is the fraction at which further turns are blocked
	// pending user action (fork / compact / abort). Default 0.90.
	HardThreshold float64 `koanf:"hard_threshold"`
}

type Defaults struct {
	Provider string `koanf:"provider"`
	Model    string `koanf:"model"`
	// Persona names the bundled or user-installed persona that
	// supplies the agent's operating manual (system prompt). Empty
	// or "default" = bundled default. Resolution order:
	// {cwd}/.stado/personas → ~/.stado/personas → bundled.
	// Per-call surfaces (CLI flag, /persona, agent.spawn arg) override.
	Persona string `koanf:"persona"`
	// AllowProjectPersona opts into honoring personas from a repo's
	// {cwd}/.stado/personas/ directory. Default false (Codex #8/#42): a
	// project-origin persona's body is injected verbatim into the system
	// prompt and its tools:/plugins: frontmatter widens the agent surface, so
	// a repo could silently take over the operating posture just by shipping
	// a .stado/personas/default.md. When false the resolver ignores the
	// project dir and falls back to the user/bundled persona of that name.
	AllowProjectPersona bool `koanf:"allow_project_persona"`
}

type Approvals struct {
	Mode      string   `koanf:"mode"`
	Allowlist []string `koanf:"allowlist"`
}

type MCP struct {
	ConfigPath string               `koanf:"config_path"`
	Servers    map[string]MCPServer `koanf:"servers"`

	// Providers wraps coding-agent CLIs that expose themselves as
	// MCP servers (e.g. `codex mcp-server`) as stado agent.Providers
	// — analogous to ACP.Providers but for agents that don't expose
	// a stdio ACP-agent mode.
	//
	//	[mcp.providers.codex-mcp]
	//	binary        = "codex"
	//	args          = ["mcp-server"]
	//	call_tool     = "codex"
	//	continue_tool = "codex-reply"
	Providers map[string]MCPProviderWrapped `koanf:"providers"`
}

// MCPServer is one entry under [mcp.servers.<name>] in config.toml.
// Either Command (stdio server) or URL (streamable HTTP) is set.
//
// Capabilities declare what the server is allowed to touch; stado maps
// them to a sandbox.Policy and launches stdio subprocesses through the
// platform runner (bubblewrap on Linux, etc.). Out-of-manifest syscalls
// fail visibly. Stdio servers must declare at least one capability;
// empty slices are rejected. HTTP servers run remotely and aren't
// sandboxed locally.
//
// Supported forms:
//
//	fs:read:<path>           read-only bind
//	fs:write:<path>          read-write bind
//	net:<host>               allow egress to host (via stado's proxy)
//	net:deny                 unshare-net (no egress)
//	net:allow                share host network
//	exec:<binary>            add binary to the exec allow-list
//	env:<VAR>                pass through the env var
//
// See DESIGN §"Phase 8.1 — per-MCP-server sandbox" / PLAN §8.1.
type MCPServer struct {
	Command      string            `koanf:"command"`
	Args         []string          `koanf:"args"`
	Env          map[string]string `koanf:"env"`
	URL          string            `koanf:"url"`
	Capabilities []string          `koanf:"capabilities"`
}

// Inference is Phase 1's [inference] section: presets for OAI-compat endpoints
// plus per-provider settings. Filled in with Phase 1.
type Inference struct {
	Presets map[string]InferencePreset `koanf:"presets"`
}

type InferencePreset struct {
	Endpoint string `koanf:"endpoint"`
	// APIKeyEnv names the environment variable that holds the API key
	// for this preset. Required for custom (non-builtin) preset names —
	// without it, stado has no way to send credentials to the
	// configured endpoint. Builtin preset names (litellm, groq, etc.)
	// keep their conventional env var when this is empty. When set, it
	// always wins over the builtin convention.
	APIKeyEnv string `koanf:"api_key_env"`
	// BaseURL is a per-provider base-URL override. It is only meaningful
	// for providers stado reaches through a base-URL-overridable client:
	// ProviderKindAnthropicCompatCloud (native anthropic SDK +
	// WithBaseURL) and the OAI-compat presets (where the override points
	// the OpenAI-compatible client at a non-default host). For pure
	// OAI-compat presets Endpoint already serves as the base URL; BaseURL
	// lets `stado auth set` record an explicit override without clobbering
	// a bundled default Endpoint. Empty means "use the provider's default".
	BaseURL string `koanf:"base_url"`
}

// Harness is the [harness] config section — operator-mode selection. EP-0030.
//
//	[harness]
//	mode = "security"   # "" (default/general) | "security"
type Harness struct {
	// Mode selects the default harness. "" or "general" = standard.
	// "security" = security-research harness (system prompt from
	// .stado/harness/security.md if present, else built-in template).
	Mode string `koanf:"mode"`
}

// Supervisor is the [supervisor] config section — responsive frontline
// supervisor/worker lane split. Off by default. EP-0033.
//
//	[supervisor]
//	enabled  = true
//	provider = "anthropic-haiku"   # references a [providers.<name>] entry
//	model    = "claude-haiku-4-5"  # optional model override
type Supervisor struct {
	// Enabled activates the supervisor lane. Default false.
	Enabled bool `koanf:"enabled"`
	// Provider is the provider entry name to use for the supervisor lane.
	// Empty = use the same provider as the worker.
	Provider string `koanf:"provider"`
	// Model overrides the supervisor provider's default model.
	// Empty = use the provider's default.
	Model string `koanf:"model"`
}

// Runtime is the [runtime] config section — internal migration flags.
// These are not operator-facing in the normal sense; they gate per-tool
// wasm parity migrations during EP-0038 rollout. All default false (use
// native Go implementations) until the golden parity test for each tool
// passes.
//
//	[runtime.use_wasm]
//	fs     = true    # flip after fs parity test passes
//	shell  = true
//	rg     = true
type Runtime struct {
	// UseWasm maps short tool-family names to booleans. When true, the
	// wasm plugin for that family is registered instead of (and with the
	// wire names replacing) the native implementation.
	// Families: "fs", "shell", "rg", "astgrep", "readctx", "lsp",
	//           "web", "http", "agent", "mcp", "image", "dns",
	//           "secrets", "task", "tools".
	UseWasm map[string]bool `koanf:"use_wasm"`
}

// Sandbox is the [sandbox] config section. EP-0037 reserves the schema;
// EP-0038 implements the wrap-mode enforcement.
//
//	[sandbox]
//	mode = "off"          # "off" | "wrap" | "external"
//	http_proxy = ""       # e.g. "http://127.0.0.1:8080"
//	dns_servers = []      # override system resolver
//	allow_env = []        # env-var allow-list; empty = pass-through
//	refuse_no_runner = false  # hard-refuse when mode=wrap but no wrapper found
type Sandbox struct {
	// Mode controls process-containment behaviour. Default "off".
	Mode string `koanf:"mode"`
	// HTTPProxy is injected as HTTP_PROXY / HTTPS_PROXY into the wrapped process.
	HTTPProxy string `koanf:"http_proxy"`
	// DNSServers overrides the system resolver inside the sandbox.
	DNSServers []string `koanf:"dns_servers"`
	// AllowEnv is an allow-list of environment variable names passed into
	// the sandbox. Empty = pass all through (default).
	AllowEnv []string `koanf:"allow_env"`
	// RefuseNoRunner makes mode=wrap hard-fail when no wrapper binary is
	// found. Default false (warn loudly, run anyway).
	RefuseNoRunner bool `koanf:"refuse_no_runner"`
	// Wrap holds [sandbox.wrap] sub-section config. EP-0038d.
	Wrap SandboxWrap `koanf:"wrap"`
}

// SandboxWrap is the [sandbox.wrap] sub-section. EP-0038d.
type SandboxWrap struct {
	// Runner selects the wrapper binary: "auto" (default), "bwrap",
	// "firejail", or "sandbox-exec".
	Runner string `koanf:"runner"`
	// BindRO is a list of paths to mount read-only inside the sandbox.
	// Additive on top of the default contract (stado XDG dirs, /usr, resolv.conf).
	BindRO []string `koanf:"bind_ro"`
	// BindRW is a list of paths to mount read-write inside the sandbox.
	// The operator's CWD is NOT auto-bound — declare it here.
	BindRW []string `koanf:"bind_rw"`
	// Network controls network access inside the sandbox.
	// "host" (default) = full access; "namespaced" = isolated netns;
	// "off" = no network at all.
	Network string `koanf:"network"`
}

// Git is Phase 2's [git] section — sidecar paths, author identity.
type Git struct{}

// OTel is Phase 6's [otel] section. Mirrors telemetry.Config shape so
// internal/telemetry can cast this straight into its config type.
type OTel struct {
	Enabled     bool              `koanf:"enabled"`
	Endpoint    string            `koanf:"endpoint"`
	Protocol    string            `koanf:"protocol"`
	Insecure    bool              `koanf:"insecure"`
	Headers     map[string]string `koanf:"headers"`
	SampleRate  float64           `koanf:"sample_rate"`
	ServiceName string            `koanf:"service_name"`
}

// ACP is Phase 8's [acp] section. Houses both server-side and
// client-side ACP knobs.
//
//	[acp.providers.gemini-acp]
//	binary = "gemini"
//	args   = ["--acp"]
//
//	[acp.providers.opencode-acp]
//	binary = "opencode"
//	args   = ["acp"]
//
// Each entry registers a stado provider that wraps an external ACP-
// speaking coding-agent CLI. The provider is built lazily on first
// use; the wrapped agent's tools live INSIDE the wrapped agent
// (phase A of EP-0032 — wrapped-agent-owns-tools). Phase B will add
// optional tool-host capability so wrapped agents can call stado's
// tool registry via ACP method calls.
type ACP struct {
	Providers map[string]ACPProvider `koanf:"providers"`

	// MaxTurns caps the agent loop's per-prompt turn budget when stado
	// runs in ACP server mode (`stado acp`). Zero means use the built-in
	// default (50 with --tools, 1 without). Callers may also override
	// this on a per-session basis via `session/new`'s `maxTurns` param;
	// that param wins when set. v0.45.1 — engagement tasks routinely
	// need 30–100+ turns and the previous hardcoded 10 throttled them.
	MaxTurns int `koanf:"max_turns"`
}

// MCPProviderWrapped is `[mcp.providers.<name>]` — wraps a CLI that
// exposes itself as an MCP server (e.g. `codex mcp-server`) as a
// stado agent.Provider. Distinct from MCP-clients-mounted-into-LLM
// (which are configured separately at runtime); this is "use CLI X
// as the LLM-driver via MCP transport" — the analogue of ACPProvider
// for MCP-only agents like codex.
//
// Example codex entry:
//
//	[mcp.providers.codex-mcp]
//	binary           = "codex"
//	args             = ["mcp-server"]
//	call_tool        = "codex"
//	continue_tool    = "codex-reply"
//	# Optional pinning of model/sandbox/etc:
//	# [mcp.providers.codex-mcp.call_tool_overrides]
//	# model = "gpt-5.2"
//	# sandbox = "workspace-write"
type MCPProviderWrapped struct {
	// Binary is the absolute path to or PATH-resolvable name of the
	// wrapped agent's executable. Required.
	Binary string `koanf:"binary"`
	// Args is the argv passed to Binary to launch its MCP server
	// mode (e.g. ["mcp-server"] for codex).
	Args []string `koanf:"args"`
	// CallTool is the MCP tool name for the FIRST turn in a session
	// (no thread id captured yet). Required.
	CallTool string `koanf:"call_tool"`
	// ContinueTool is the MCP tool name for SUBSEQUENT turns. When
	// empty the wrapped agent is treated as stateless — every turn
	// calls CallTool fresh.
	ContinueTool string `koanf:"continue_tool"`
	// PromptArgKey overrides the input field name for the user
	// prompt (default "prompt").
	PromptArgKey string `koanf:"prompt_arg_key"`
	// ThreadIDArgKey overrides the input field name for the thread
	// id on continuation calls (default "threadId").
	ThreadIDArgKey string `koanf:"thread_id_arg_key"`
	// ContentResultKey overrides the output field name for the
	// assistant text (default "content").
	ContentResultKey string `koanf:"content_result_key"`
	// ThreadIDResultKey overrides the output field name for the
	// captured thread id (default "threadId").
	ThreadIDResultKey string `koanf:"thread_id_result_key"`
	// CallToolOverrides is merged into every tools/call's arguments
	// — pin model, sandbox, approval-policy, etc. The prompt key is
	// always supplied by stado and cannot be overridden here.
	CallToolOverrides map[string]any `koanf:"call_tool_overrides"`
	// Env adds explicit `KEY=value` entries to the wrapped MCP
	// subprocess's environment. Layered on top of the envscrub
	// safelist core + InheritEnv extracts. v0.57.0 reconciliation
	// (see decision file
	// .agent/decisions/2026-05-25-acpwrap-inherit-env-opt-in.md) —
	// brings MCPProviderWrapped to schema parity with ACPProvider.
	Env []string `koanf:"env"`
	// InheritEnv lists env-var NAMES to forward from the parent
	// process to the wrapped MCP subprocess. Example:
	//
	//   [mcp.providers.codex-mcp]
	//   binary = "codex"
	//   args = ["mcp-server"]
	//   call_tool = "codex"
	//   continue_tool = "codex-reply"
	//   inherit_env = ["OPENAI_API_KEY"]
	//
	// Required for wrapped agents that need parent-shell auth state.
	// Env entries above win on duplicate keys.
	InheritEnv []string `koanf:"inherit_env"`
}

// ACPProvider declares one wrapped-agent provider. Binary is the
// only required field; everything else inherits stado defaults.
type ACPProvider struct {
	// Binary is the absolute path to (or PATH-resolvable name of)
	// the wrapped agent's executable. Required.
	Binary string `koanf:"binary"`
	// Args is the argv passed to Binary to launch its ACP server
	// mode (e.g. ["--acp"] for gemini, ["acp"] for opencode).
	Args []string `koanf:"args"`
	// CWD overrides the working directory the wrapped agent reports
	// for its session. Empty = stado's cwd at first-stream time.
	CWD string `koanf:"cwd"`
	// Env adds explicit `KEY=value` entries to the wrapped agent's
	// environment. Layered on top of the envscrub safelist core
	// (HOME / PATH / USER / TERM / XDG_*) and any `InheritEnv` opt-
	// in extracts. Pre-v0.56.0 stado inherited the parent env
	// unconditionally (per EP-0032); v0.56.0 PR #65 scrubbed without
	// flagging the breaking change; v0.57.0 added `InheritEnv` to
	// reconcile (decision file:
	// .agent/decisions/2026-05-25-acpwrap-inherit-env-opt-in.md).
	Env []string `koanf:"env"`
	// InheritEnv lists env-var NAMES from the parent process to
	// forward to the wrapped agent — per EP-0032's "operator's job
	// to manage env" trust model. Common usage:
	//
	//   [acp.providers.gemini-acp]
	//   binary = "gemini"
	//   args = ["--acp"]
	//   inherit_env = ["GEMINI_API_KEY"]
	//
	//   [acp.providers.claude-code-acp]
	//   binary = "claude-code-acp"
	//   inherit_env = ["ANTHROPIC_API_KEY"]
	//
	// Required for wrapped agents that read auth state from the
	// parent shell. Explicit `Env` entries above win on duplicate
	// keys (so an operator can pin a sandbox/CI-specific value over
	// an inherited one).
	InheritEnv []string `koanf:"inherit_env"`
	// Tools selects the tool-host policy (EP-0032 phase B).
	//   "" / "agent" — default; wrapped agent uses its own tools.
	//   "stado"      — stado advertises fs.read/write capabilities
	//                  AND mounts itself as MCP server in
	//                  session/new.mcpServers; the wrapped agent's
	//                  tool calls through these channels route
	//                  through stado's Executor + sandbox runner.
	Tools string `koanf:"tools"`
	// RegisterMCP grants operator consent for stado to auto-write the wrapped
	// agent's user-scope MCP config so the agent can call stado back as an MCP
	// server (EP-0032). Without it, CheckMCPRegistration only prints a manual-
	// setup hint and never writes any file. Default false (no auto-write).
	RegisterMCP bool `koanf:"register_mcp"`
}

// Plugins is Phase 7's [plugins] section. CRL fields are Phase 7.6 —
// the revocation list is downloaded from CRLURL, verified against
// CRLIssuerPubkey (hex- or base64-encoded Ed25519), and consulted
// during `stado plugin verify` / install.
type Plugins struct {
	// CRLURL points at a signed JSON CRL (stado serves a public one;
	// airgap users can self-host). Empty = CRL checks disabled.
	CRLURL string `koanf:"crl_url"`
	// CRLIssuerPubkey is the Ed25519 key the CRL is signed with. Required
	// when CRLURL is set — empty disables verification and falls back to
	// the trust-store-only gate with a stderr advisory.
	CRLIssuerPubkey string `koanf:"crl_issuer_pubkey"`
	// RekorURL points at a Rekor transparency-log instance (e.g.
	// `https://rekor.sigstore.dev`). When set, `stado plugin verify`
	// consults Rekor for a matching hashedrekord entry — proof that the
	// manifest signature was logged before install. Empty = advisory
	// only, no Rekor lookup.
	RekorURL string `koanf:"rekor_url"`

	// Background lists installed plugin IDs (`<name>-<version>`) to
	// load as persistent background plugins for each new TUI session.
	// The bundled `auto-compact` background plugin is loaded by
	// default even when this list is empty; this slice is additive for
	// extra installed plugins. A background plugin must export
	// `stado_plugin_tick` — the TUI calls it once per event boundary
	// so the plugin can observe session events + react
	// (auto-compaction, telemetry bridges, session recorders). DESIGN
	// §"Plugin extension points for context management" has the full
	// contract.
	Background []string `koanf:"background"`

	// AllowProjectPlugins opts into autoloading signed plugins from a repo's
	// {cwd}/.stado/plugins/ directory. Default false (Codex #4/#45): even
	// though signature verification still applies, autoloading a project-local
	// plugin exposes its tool descriptions to the model and lets it shadow
	// global/built-in tools, and a project plugin declaring `fs:write:.` gets
	// the whole project tree — too much for an untrusted repo to enable on a
	// bare `cd`. When false, only the global plugin dir is autoloaded.
	AllowProjectPlugins bool `koanf:"allow_project_plugins"`
}

func Load() (*Config, error) {
	k := koanf.New(".")

	configPath := defaultConfigPath()
	// MkdirAllUnderExistingAncestor: walk up from the desired config
	// dir to the longest existing ancestor (typically the user's
	// HOME or XDG_CONFIG_HOME), then create everything below with
	// no-symlink enforcement. The plain MkdirAllNoSymlink walks from
	// `/`, which fails on systems where `/home` is a symlink to
	// `/var/home` (Fedora Atomic / Silverblue) — the user's ancestor
	// environment is operator-controlled and trusted; only the path
	// below it needs adversarial-symlink defense. EP-0028.
	uc := workdirpath.NewUserConfigResolver()
	if err := uc.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	if _, err := os.Lstat(configPath); err == nil {
		data, err := uc.ReadFileLimited(configPath, maxConfigBytes)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		if err := k.Load(staticBytesProvider(data), toml.Parser()); err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat config: %w", err)
	}

	// EP-0035: project-local .stado/config.toml overlay.
	// Loaded after user config so project settings win key-by-key within
	// each table. Env vars (next step) still win over both.
	cwd, _ := os.Getwd()
	projectStadoDir := findProjectStadoDir(cwd)
	if projectStadoDir != "" {
		projectCfgPath := filepath.Join(projectStadoDir, "config.toml")
		if info, err := os.Lstat(projectCfgPath); err == nil && info.Mode().IsRegular() {
			data, err := os.ReadFile(projectCfgPath) //nolint:gosec // path is inside user-controlled cwd
			if err != nil {
				return nil, fmt.Errorf("load project config: %w", err)
			}
			if int64(len(data)) > maxConfigBytes {
				return nil, fmt.Errorf("project config exceeds %d byte limit", maxConfigBytes)
			}
			// Load the project overlay into a SEPARATE instance first so we can
			// strip security-sensitive keys before merging. A repo is untrusted
			// input (threat model: "Repository contents ... attacker-controlled").
			// The keys below have no legitimate project use, or must never be
			// honored from a repo even if the project is otherwise trusted:
			//   hooks / aliases     arbitrary shell / slash-command exec (#040/#060/#002)
			//   keymap              can remap or neutralize the Esc/Ctrl+G interrupt
			//                       kill switch, or swap the whole input model (#7/#10)
			//   defaults.persona    selects a repo .stado/personas/*.md whose body is
			//                       injected verbatim into the system prompt (#8/#42)
			//   defaults.allow_project_persona  the persona opt-in gate itself —
			//                       a repo must not be able to self-enable project
			//                       personas by committing this key
			//   agent.system_prompt_path  points loadSystemPromptTemplate at a repo
			//                       file → repo-controlled provider system prompt
			//   plugins.background  auto-runs installed wasm plugins at launch (#8)
			//   plugins.allow_project_plugins  the project-plugin autoload opt-in
			//                       gate itself — a repo must not self-enable it (#4)
			//   acp                 register_mcp = persistent MCP backdoor (#3),
			//                       inherit_env = host-secret passthrough (#20),
			//                       max_turns = removes the loop circuit-breaker (#54)
			//   mcp.providers / mcp.servers  wrapped-MCP inherit_env host-secret
			//                       passthrough (#20) + repo-declared MCP subprocess
			//                       servers ([mcp.servers.x] command=… is an exec
			//                       vector); operator declares MCP servers, not a repo
			//   tui.sidebar/footer  can hide the sandbox/budget/risk safety chrome (#14)
			//   lsp.auto_diagnostics  the LSP-spawn opt-in gate itself — a repo must
			//                       not be able to re-enable unsandboxed LSP spawns (#12)
			//   sandbox             a repo must never weaken the process-containment
			//                       posture (mode/proxy/dns/allow_env) — EP-0044 phase 2
			//   runtime             use_wasm flips native↔wasm tool impls; a repo
			//                       swapping implementations is an operator-only call
			//   inference           [inference.presets.x] endpoint + api_key_env is an
			//                       API-key exfil vector — operator declares endpoints
			// Project model/provider/tool overrides (the EP-0035 use case) stay
			// (defaults.model/provider pick among USER-defined providers).
			pk := koanf.New(".")
			if err := pk.Load(staticBytesProvider(data), toml.Parser()); err != nil {
				return nil, fmt.Errorf("load project config: %w", err)
			}
			// Strip CASE-INSENSITIVELY (Codex #215 P1): koanf's Exists/Delete are
			// case-sensitive, but the final mapstructure Unmarshal matches section
			// names case-insensitively. So a repo committing `[Sandbox]` /
			// `[MCP.servers.x]` / `[Defaults] Persona=…` would survive a
			// case-sensitive Delete yet still populate the tagged field — a strip
			// bypass. Match each real (actual-cased) leaf key against the
			// lower-cased strip set as an exact key or a table prefix, and delete
			// the real leaf. Whole-table entries ("acp") strip every "acp.*" leaf.
			stripKeys := []string{
				"hooks", "aliases", "keymap",
				"defaults.persona", "defaults.allow_project_persona",
				"agent.system_prompt_path",
				"plugins.background", "plugins.allow_project_plugins",
				"acp", "mcp.providers", "mcp.servers",
				"tui.sidebar", "tui.footer",
				"lsp.auto_diagnostics",
				"sandbox", "runtime", "inference", "verify",
			}
			warned := map[string]bool{}
			for _, leaf := range pk.Keys() {
				ll := strings.ToLower(leaf)
				for _, sk := range stripKeys {
					if ll == sk || strings.HasPrefix(ll, sk+".") {
						pk.Delete(leaf)
						if !warned[sk] {
							warned[sk] = true
							fmt.Fprintf(os.Stderr, "stado: ignoring %q from project .stado/config.toml — not honored from a repo (security). Set it in your user/global config instead.\n", sk)
						}
						break
					}
				}
			}
			if err := k.Merge(pk); err != nil {
				return nil, fmt.Errorf("merge project config: %w", err)
			}
		}
	}

	if err := k.Load(env.Provider("STADO_", ".", func(s string) string {
		key := strings.ToLower(strings.TrimPrefix(s, "STADO_"))
		return strings.ReplaceAll(key, "_", ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	cfg.ConfigPath = configPath
	cfg.projectStadoDir = projectStadoDir
	if err := cfg.loadSystemPromptTemplate(); err != nil {
		return nil, err
	}

	// No hardcoded provider/model defaults. An empty Defaults.Provider
	// is the signal for buildProvider to probe local inference runners
	// (ollama / lmstudio / llamacpp / vllm / user presets) and pick
	// the first reachable one. If the user wants anthropic / openai /
	// google, they set it explicitly in config or STADO_DEFAULTS_*.
	// This keeps stado from assuming a specific hosted provider as
	// the canonical default.
	if cfg.Approvals.Mode == "" {
		cfg.Approvals.Mode = "prompt"
	}
	if cfg.Agent.Thinking == "" {
		cfg.Agent.Thinking = "auto"
	}
	if cfg.Agent.ThinkingBudgetTokens == 0 {
		cfg.Agent.ThinkingBudgetTokens = 16384
	}
	if len(cfg.Verify.Commands) > 0 && cfg.Verify.MaxRounds == 0 {
		cfg.Verify.MaxRounds = 3
	}
	if cfg.Verify.MaxRounds < 0 {
		return nil, fmt.Errorf("verify.max_rounds must be >= 0")
	}
	cfg.TUI.ThinkingDisplay = normalizeThinkingDisplay(cfg.TUI.ThinkingDisplay)
	cfg.TUI.ToolDisplay = normalizeToolDisplay(cfg.TUI.ToolDisplay)
	// R9: distinguish "unset" (apply default) from an explicit 0 (disable the
	// gate, per docs/features/context.md) — a bare `== 0` check can't, since
	// both produce the float64 zero value after unmarshal. Default only when
	// the key was never set in TOML or env.
	if !k.Exists("context.soft_threshold") {
		cfg.Context.SoftThreshold = 0.70
	}
	if !k.Exists("context.hard_threshold") {
		cfg.Context.HardThreshold = 0.90
	}
	// Memory retrieval is on by default. Distinguish "unset" (apply the default)
	// from an explicit `enabled = false` (a deliberate opt-out), exactly like
	// the context thresholds above — a bare zero-value check could not.
	if !k.Exists("memory.enabled") {
		cfg.Memory.Enabled = true
	}
	// Budget sanity: if both thresholds are set but the hard cap is at
	// or below the warn cap, the warning would never fire. Drop the
	// hard cap back to zero ("no hard limit") and announce so the user
	// can fix their config — better than silently blocking turns that
	// the user thought would just warn.
	if cfg.Budget.HardUSD > 0 && cfg.Budget.WarnUSD > 0 && cfg.Budget.HardUSD <= cfg.Budget.WarnUSD {
		fmt.Fprintf(os.Stderr,
			"stado: [budget] hard_usd=%.2f must be > warn_usd=%.2f — ignoring hard_usd\n",
			cfg.Budget.HardUSD, cfg.Budget.WarnUSD)
		cfg.Budget.HardUSD = 0
	}

	return &cfg, nil
}

type staticBytesProvider []byte

func (p staticBytesProvider) ReadBytes() ([]byte, error) {
	out := make([]byte, len(p))
	copy(out, p)
	return out, nil
}

func (p staticBytesProvider) Read() (map[string]any, error) {
	return nil, errors.New("static bytes provider does not support parsed reads")
}

// normalizeDisplayMode canonicalizes a display-mode config value to one of
// preview/auto/collapsed/expanded. Empty -> preview (the default). Legacy
// thinking_display values (show/tail/hide) map to the nearest mode so old
// configs keep loading. Unrecognized values warn (using key for the
// message) and fall back to preview.
func normalizeDisplayMode(key, value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "preview":
		return "preview"
	case "auto":
		return "auto"
	case "collapsed":
		return "collapsed"
	case "expanded":
		return "expanded"
	// Legacy thinking_display vocabulary.
	case "tail":
		return "preview"
	case "show", "full", "on":
		return "expanded"
	case "hide", "off":
		return "collapsed"
	default:
		fmt.Fprintf(os.Stderr,
			"stado: [tui] %s=%q is invalid; using \"preview\"\n",
			key, value)
		return "preview"
	}
}

func normalizeThinkingDisplay(value string) string {
	return normalizeDisplayMode("thinking_display", value)
}

func normalizeToolDisplay(value string) string {
	return normalizeDisplayMode("tool_display", value)
}

// System-prompt-template lifecycle moved to system_prompt_template.go.
// Path resolution helpers (StateDir, WorktreeDir, ConfigDir, etc.)
// moved to paths.go.
