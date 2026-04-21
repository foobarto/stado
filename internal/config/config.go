package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const appName = "stado"

// Config is the top-level stado configuration.
//
// Phase 0 scaffold: legacy [providers.*], [context], [embeddings] sections are
// gone; [inference], [sandbox], [git], [otel], [acp], [plugins] are placeholders
// that later phases fill in (see PLAN.md).
type Config struct {
	ConfigPath string `koanf:"-"`

	Defaults  Defaults  `koanf:"defaults"`
	Approvals Approvals `koanf:"approvals"`
	MCP       MCP       `koanf:"mcp"`

	Inference Inference `koanf:"inference"`
	Sandbox   Sandbox   `koanf:"sandbox"`
	Git       Git       `koanf:"git"`
	OTel      OTel      `koanf:"otel"`
	ACP       ACP       `koanf:"acp"`
	Plugins   Plugins   `koanf:"plugins"`
	Context   Context   `koanf:"context"`
	Agent     Agent     `koanf:"agent"`
	Tools     Tools     `koanf:"tools"`
	Budget    Budget    `koanf:"budget"`
	Hooks     Hooks     `koanf:"hooks"`
}

// Hooks is the [hooks] config section — user-provided shell commands
// fired at TUI lifecycle events. MVP scope: notification-only.
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
	// PostTurn fires after every completed TUI turn (EvDone → drain).
	// Empty = no hook.
	PostTurn string `koanf:"post_turn"`
}

// Budget is the [budget] config section — per-session cost guardrails.
// Stado already tracks CostUSD on every provider turn; this adds two
// thresholds to surface a warning and (optionally) hard-block new
// turns. Both default to 0, meaning "no limit" — the guardrail is
// opt-in so cost-insensitive local-runner users don't see a pill for
// nothing.
//
//	[budget]
//	warn_usd = 1.00   # status-bar pill + one-time system block when crossed
//	hard_usd = 5.00   # block further turns pending user ack
//
// Fractional dollars allowed. A hard_usd below warn_usd is a config
// error and is ignored with a stderr warning — the guard would never
// warn before blocking.
type Budget struct {
	WarnUSD float64 `koanf:"warn_usd"`
	HardUSD float64 `koanf:"hard_usd"`
}

// Tools is the [tools] config section — user-level control over
// which bundled tools are visible to the agent. All tools are
// available by default. Either list is accepted; Enabled wins when
// both are set (it's an explicit allowlist so mentioning Disabled
// alongside is redundant).
//
//   [tools]
//   enabled  = ["read", "grep", "bash"]    # only these — allowlist mode
//   disabled = ["webfetch"]                 # remove specific tools from default set
//
// Tool names match the `Name()` each bundled tool returns (see
// internal/runtime/runtime.go BuildDefaultRegistry for the
// canonical list). Unknown names in either list are ignored with a
// warning on stderr — tolerates typos without refusing to boot.
type Tools struct {
	Enabled  []string `koanf:"enabled"`
	Disabled []string `koanf:"disabled"`
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
}

// MCPServer is one entry under [mcp.servers.<name>] in config.toml.
// Either Command (stdio server) or URL (streamable HTTP) is set.
//
// Capabilities declare what the server is allowed to touch; stado maps
// them to a sandbox.Policy and launches the stdio subprocess through the
// platform runner (bubblewrap on Linux, etc.). Out-of-manifest syscalls
// fail visibly. Empty slice = unsandboxed (backwards-compat default).
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
}

// Sandbox is Phase 3's [sandbox] section — placeholder.
type Sandbox struct{}

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

// ACP is Phase 8's [acp] section.
type ACP struct{}

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
	// A background plugin must export `stado_plugin_tick` — the TUI
	// calls it once per turn boundary so the plugin can observe
	// session events + react (auto-compaction, telemetry bridges,
	// session recorders). DESIGN §"Plugin extension points for
	// context management" has the full contract.
	Background []string `koanf:"background"`
}

func Load() (*Config, error) {
	k := koanf.New(".")

	configPath := defaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	if _, err := os.Stat(configPath); err == nil {
		if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
			return nil, fmt.Errorf("load config: %w", err)
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

func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName, "config.toml")
}

func (c *Config) StateDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", appName)
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
