package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

const appName = "stado"
const maxConfigBytes int64 = 1 << 20
const maxSystemPromptTemplateBytes int64 = 1 << 20

// Config is the top-level stado configuration.
//
// Phase 0 scaffold: legacy [providers.*], [context], [embeddings] sections are
// gone; [inference], [sandbox], [git], [otel], [acp], [plugins] are placeholders
// that later phases fill in (see PLAN.md).
type Config struct {
	ConfigPath string `koanf:"-"`
	// projectStadoDir is the absolute path of the .stado/ directory
	// found by walking up from cwd. Empty when no .stado/ exists.
	// EP-0035.
	projectStadoDir string

	Defaults  Defaults  `koanf:"defaults"`
	Approvals Approvals `koanf:"approvals"`
	MCP       MCP       `koanf:"mcp"`

	Inference Inference `koanf:"inference"`
	Sandbox   Sandbox   `koanf:"sandbox"`
	Git       Git       `koanf:"git"`
	OTel      OTel      `koanf:"otel"`
	ACP       ACP       `koanf:"acp"`
	Plugins   Plugins   `koanf:"plugins"`
	Memory    Memory    `koanf:"memory"`
	Context   Context   `koanf:"context"`
	Agent     Agent     `koanf:"agent"`
	Sampling  Sampling  `koanf:"sampling"`
	Sessions  Sessions  `koanf:"sessions"`
	TUI       TUI       `koanf:"tui"`
	Tools     Tools     `koanf:"tools"`
	Budget    Budget    `koanf:"budget"`
	Hooks     Hooks     `koanf:"hooks"`
	Runtime    Runtime    `koanf:"runtime"`
	Supervisor Supervisor `koanf:"supervisor"`
	Harness    Harness    `koanf:"harness"`
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
	// provider system prompts. Off by default until users deliberately
	// opt in to long-lived context.
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
	// rendered in the viewport: show, tail, or hide.
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
	// Env adds entries to the wrapped agent's environment (parent
	// PATH/HOME/etc inherit by default).
	Env []string `koanf:"env"`
	// Tools selects the tool-host policy (EP-0032 phase B).
	//   "" / "agent" — default; wrapped agent uses its own tools.
	//   "stado"      — stado advertises fs.read/write capabilities
	//                  AND mounts itself as MCP server in
	//                  session/new.mcpServers; the wrapped agent's
	//                  tool calls through these channels route
	//                  through stado's Executor + sandbox runner.
	Tools string `koanf:"tools"`
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
	if err := workdirpath.MkdirAllUnderUserConfig(filepath.Dir(configPath), 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	if _, err := os.Lstat(configPath); err == nil {
		data, err := workdirpath.ReadRegularFileUnderUserConfigLimited(configPath, maxConfigBytes)
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
			if err := k.Load(staticBytesProvider(data), toml.Parser()); err != nil {
				return nil, fmt.Errorf("load project config: %w", err)
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
	cfg.TUI.ThinkingDisplay = normalizeThinkingDisplay(cfg.TUI.ThinkingDisplay)
	if cfg.Context.SoftThreshold == 0 {
		cfg.Context.SoftThreshold = 0.70
	}
	if cfg.Context.HardThreshold == 0 {
		cfg.Context.HardThreshold = 0.90
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

func normalizeThinkingDisplay(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "show", "full", "on":
		return "show"
	case "tail":
		return "tail"
	case "hide", "off":
		return "hide"
	default:
		fmt.Fprintf(os.Stderr,
			"stado: [tui] thinking_display=%q is invalid; using \"show\"\n",
			value)
		return "show"
	}
}

const defaultSystemPromptFilename = "system-prompt.md"
const legacyDefaultSystemPromptTemplateSHA256 = "e712fed3c1f394afa61cb4f078fe3bde7acee8a902e75ab5914753aafcf04188"

func (c *Config) loadSystemPromptTemplate() error {
	explicitPath := strings.TrimSpace(c.Agent.SystemPromptPath) != ""
	if !explicitPath {
		c.Agent.SystemPromptPath = filepath.Join(filepath.Dir(c.ConfigPath), defaultSystemPromptFilename)
		if err := ensureDefaultSystemPromptTemplate(c.Agent.SystemPromptPath); err != nil {
			return err
		}
	} else {
		c.Agent.SystemPromptPath = expandHome(c.Agent.SystemPromptPath)
	}
	var body []byte
	var err error
	body, err = workdirpath.ReadRegularFileUnderUserConfigLimited(c.Agent.SystemPromptPath, maxSystemPromptTemplateBytes)
	if err != nil {
		return fmt.Errorf("load [agent].system_prompt_path %s: %w", c.Agent.SystemPromptPath, err)
	}
	if err := instructions.ValidateSystemPromptTemplate(string(body)); err != nil {
		return fmt.Errorf("validate [agent].system_prompt_path %s: %w", c.Agent.SystemPromptPath, err)
	}
	c.Agent.SystemPromptTemplate = string(body)
	return nil
}

func ensureDefaultSystemPromptTemplate(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("default system prompt template is a symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("default system prompt template is not a regular file: %s", path)
		}
		data, err := workdirpath.ReadRegularFileUnderUserConfigLimited(path, maxSystemPromptTemplateBytes)
		if err != nil {
			return fmt.Errorf("read default system prompt template: %w", err)
		}
		if isLegacyDefaultSystemPromptTemplate(data) {
			if err := replaceDefaultSystemPromptTemplate(path); err != nil {
				return fmt.Errorf("update default system prompt template: %w", err)
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat default system prompt template: %w", err)
	}
	if err := workdirpath.MkdirAllUnderUserConfig(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create system prompt template dir: %w", err)
	}
	if err := createDefaultSystemPromptTemplate(path); err != nil {
		return fmt.Errorf("write default system prompt template: %w", err)
	}
	return nil
}

func createDefaultSystemPromptTemplate(path string) error {
	root, name, err := systemPromptTemplateRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := writeSystemPromptTemplateFile(f); err != nil {
		_ = f.Close()
		_ = root.Remove(name)
		return err
	}
	return nil
}

func replaceDefaultSystemPromptTemplate(path string) error {
	root, name, err := systemPromptTemplateRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmpName := "." + name + "." + uuid.NewString() + ".tmp"
	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmpName)
		}
	}()
	if err := writeSystemPromptTemplateFile(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

func writeSystemPromptTemplateFile(f *os.File) error {
	body := []byte(instructions.DefaultSystemPromptTemplate)
	n, err := f.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func systemPromptTemplateRoot(path string) (*os.Root, string, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", fmt.Errorf("invalid system prompt template path: %s", path)
	}
	root, err := workdirpath.OpenRootUnderUserConfig(filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

func isLegacyDefaultSystemPromptTemplate(data []byte) bool {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]) == legacyDefaultSystemPromptTemplateSHA256
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// DefaultConfigPath returns the operator-level config file location:
// $XDG_CONFIG_HOME/stado/config.toml, falling back to
// ~/.config/stado/config.toml. Mirrors what config.Load() reads when
// no project-local override is present. Exported so CLI subcommands
// like `stado tool enable --global` can target the same file.
func DefaultConfigPath() string { return defaultConfigPath() }

func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName, "config.toml")
}

// findProjectStadoDir walks from start upward looking for a directory
// that contains a `.stado/` subdirectory. Returns the `.stado/` path
// when found, or "" when nothing is found up to the filesystem root.
// EP-0035.
func findProjectStadoDir(start string) string {
	abs, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, ".stado")
		info, err := os.Lstat(candidate)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func (c *Config) StateDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", appName)
}

// ProjectStadoDir returns the absolute path of the .stado/ directory
// found by walking up from cwd at Load() time, or "" when none exists.
// EP-0035.
func (c *Config) ProjectStadoDir() string { return c.projectStadoDir }

// ProjectPluginsDir returns the per-project plugin search directory
// (.stado/plugins/) when a .stado/ directory was found, or "" when none
// exists. The directory may not exist yet — callers should check before
// listing it. EP-0035.
func (c *Config) ProjectPluginsDir() string {
	if c.projectStadoDir == "" {
		return ""
	}
	return filepath.Join(c.projectStadoDir, "plugins")
}

// AllPluginDirs returns all directories to search for installed plugins,
// in priority order: project-local first (so project plugins shadow
// global ones with the same name+version), then global. Empty entries
// are filtered out. Callers should search all returned dirs and use the
// first match. EP-0035.
func (c *Config) AllPluginDirs() []string {
	global := filepath.Join(c.StateDir(), "plugins")
	project := c.ProjectPluginsDir()
	if project == "" {
		return []string{global}
	}
	return []string{project, global}
}

// WorktreeDir is the root under which per-session worktrees live. Uses
// XDG_STATE_HOME (volatile user state) per PLAN.md §2.1.
func (c *Config) WorktreeDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "worktrees")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", appName, "worktrees")
}

// SidecarPath returns the bare-repo path for the user repo rooted at
// userRepoRoot (or cwd if empty). Filename is stable-hashed via RepoID.
func (c *Config) SidecarPath(userRepoRoot, repoID string) string {
	return filepath.Join(c.StateDir(), "sessions", repoID+".git")
}
