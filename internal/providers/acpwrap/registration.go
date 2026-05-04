package acpwrap

// MCP-registration auto-setup: when Tools = "stado" is set, the
// wrapped agent might require stado to be pre-registered as an MCP
// server in its own config (gemini's case) — without that
// registration, the agent silently ignores
// `session/new.mcpServers` and the stado-tools integration degrades
// to fs/* capabilities only.
//
// At provider startup, this layer detects which agent is being
// wrapped (from the Binary path basename), checks whether stado is
// already registered via the agent's `mcp list` command, and if
// missing, automatically runs the `mcp add` command at user scope
// to register it. Each step emits a one-line stderr log so the
// operator can see what happened.
//
// Auto-registration writes to the user's global config for the
// wrapped agent (~/.gemini/settings.json,
// ~/.claude.json or similar, ~/.codex/config.toml). To prevent
// stado from registering itself, deregister manually after the fact
// (`<agent> mcp remove stado`) or pin a different agent. There is
// no opt-out flag yet — registration is idempotent (no-op when
// already present) and reversible.
//
// Findings backing the per-agent table below come from
// docs/acp-agent-compatibility.md — see that file for the smoke
// test results and rationale.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agentMCPCheck describes how to verify and auto-register stado as
// an MCP server with one wrapped-agent CLI. Honors == true means
// the agent honors session/new.mcpServers from the ACP wire and no
// preregistration is needed; for those we skip the check entirely.
type agentMCPCheck struct {
	// Display name for the warning prefix.
	Name string
	// Honors is true when the agent honors session/new.mcpServers
	// directly (opencode, zed); false when it requires
	// preregistration (gemini, claude, codex). For Honors=true
	// agents, no check or registration runs.
	Honors bool
	// ConfigPath is the absolute path (with ~ expanded) to the
	// agent's user-scope config file. We read it directly to check
	// whether stado is registered — bypasses bugs in `mcp list`
	// commands (gemini-cli's `mcp list` was observed silently
	// returning empty even with stado registered in
	// ~/.gemini/settings.json). nil means no config-file check
	// available; we'll fall back to ListCmd or just always run the
	// idempotent `mcp add`.
	ConfigPath string
	// ConfigFormat is the parser to apply to ConfigPath: "json" or
	// "toml".
	ConfigFormat string
	// ConfigStadoPresent inspects the parsed config and returns
	// whether stado is registered as an MCP server. Per-agent
	// because the schema differs.
	ConfigStadoPresent func(parsed any) bool
	// RegisterArgs is the argv passed to the agent's mcp-add command
	// (without the binary itself — that's prepended). {STADO_BIN}
	// in any element is substituted with the absolute path to the
	// running stado binary at registration time. Argv form (not a
	// shell string) so we don't need shell escaping for paths with
	// spaces. Add commands across the surveyed agents are all
	// idempotent — running with an existing name is benign — so
	// even with no config-file pre-check, behaviour is correct.
	RegisterArgs []string
	// RegisterDescription is the human-readable command form
	// surfaced in the stderr log when auto-registration runs. Same
	// {STADO_BIN} placeholder as RegisterArgs.
	RegisterDescription string
}

// agentChecksByBinary maps the lowercased basename of `Binary` to
// its check rules. Add new entries as more agents are supported.
var agentChecksByBinary = map[string]agentMCPCheck{
	"opencode": {
		Name:   "opencode",
		Honors: true, // honors session/new.mcpServers; no setup needed
	},
	"zed": {
		Name:   "zed",
		Honors: true, // canonical ACP client per spec
	},
	"gemini": {
		Name:                "gemini",
		Honors:              false,
		ConfigPath:          "~/.gemini/settings.json",
		ConfigFormat:        "json",
		ConfigStadoPresent:  jsonMcpServersHasStado,
		RegisterArgs:        []string{"mcp", "add", "-s", "user", "stado", "{STADO_BIN}", "mcp-server"},
		RegisterDescription: "gemini mcp add -s user stado {STADO_BIN} mcp-server",
	},
	"claude": {
		Name:                "claude",
		Honors:              false,
		ConfigPath:          "~/.claude.json",
		ConfigFormat:        "json",
		ConfigStadoPresent:  jsonMcpServersHasStado,
		RegisterArgs:        []string{"mcp", "add", "-s", "user", "stado", "{STADO_BIN}", "mcp-server"},
		RegisterDescription: "claude mcp add -s user stado {STADO_BIN} mcp-server",
	},
	"codex": {
		Name:                "codex",
		Honors:              false,
		ConfigPath:          "~/.codex/config.toml",
		ConfigFormat:        "toml",
		ConfigStadoPresent:  tomlMcpServersHasStado,
		RegisterArgs:        []string{"mcp", "add", "stado", "--", "{STADO_BIN}", "mcp-server"},
		RegisterDescription: "codex mcp add stado -- {STADO_BIN} mcp-server",
	},
	"hermes": {
		Name:   "hermes",
		Honors: false,
		// hermes-cli's MCP shape is unverified on the test host;
		// no register command wired yet — falls into the warning
		// branch in CheckMCPRegistration.
	},
}

// jsonMcpServersHasStado is the parser for gemini/claude's JSON
// config — both store user-scope MCP servers under top-level
// "mcpServers" as `{<name>: {command, args, ...}}`.
func jsonMcpServersHasStado(parsed any) bool {
	root, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return false
	}
	_, present := servers["stado"]
	return present
}

// tomlMcpServersHasStado parses codex's `[mcp_servers.stado]`
// section header presence as raw bytes — we don't import a TOML
// library just for this check (the parser argument is the raw file
// contents wrapped in []byte). Robust against table-array entries,
// quoted/unquoted keys, and section-comment lines.
func tomlMcpServersHasStado(parsed any) bool {
	raw, ok := parsed.([]byte)
	if !ok {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[mcp_servers.stado]") ||
			strings.HasPrefix(trimmed, "[mcp_servers.\"stado\"]") {
			return true
		}
	}
	return false
}

// CheckMCPRegistration verifies the wrapped agent has stado
// registered as an MCP server, and auto-registers it if missing.
// Behavior:
//
//   - Agent's binary basename matches a known Honors=true agent →
//     no-op (session/new.mcpServers works directly).
//   - Agent matches a Honors=false agent with ConfigPath set →
//     read the config file. If stado already present, no-op. If
//     absent (or config missing), run RegisterArgs and emit a
//     stderr line announcing the action (so the operator knows
//     their agent's config was modified).
//   - Agent doesn't match any known entry → no-op (we don't know
//     what to check; let the existing wire-level fallback handle
//     it).
//
// All steps are best-effort and emit only stderr — never an error.
// The wrapped agent will still launch and stado's
// session/new.mcpServers entry will still be sent regardless of
// whether registration succeeded, so a failed auto-registration
// degrades cleanly into "agent uses its own tools" rather than
// blocking the launch.
//
// stadoBin is the absolute path to the running stado binary
// (typically os.Executable() result from BuildStadoMCPMount).
func CheckMCPRegistration(ctx context.Context, agentBinary, stadoBin string) {
	check, ok := lookupAgentCheck(agentBinary)
	if !ok {
		return
	}
	if check.Honors {
		return
	}
	if len(check.RegisterArgs) == 0 {
		fmt.Fprintf(os.Stderr,
			"acpwrap: warning: %s wrap with tools=\"stado\" requested, but no MCP-registration command is implemented for this agent. The session/new.mcpServers wire entry will be sent regardless; if the agent doesn't honor it, stado's tools won't be visible.\n",
			check.Name)
		return
	}

	// Check the agent's config file for an existing stado entry.
	// Idempotency guard: skips the register exec + the noisy stderr
	// line on every subsequent run after first registration.
	if check.ConfigPath != "" && check.ConfigStadoPresent != nil {
		registered, err := isStadoRegisteredInConfig(check)
		if err == nil && registered {
			return
		}
		// On err (config file missing, parse failure) — fall through
		// to register. The agent's `mcp add` is idempotent so this
		// is benign in the worst case.
	}

	regArgs := substituteStadoBin(check.RegisterArgs, stadoBin)
	regCtx, regCancel := context.WithTimeout(ctx, 15*time.Second)
	defer regCancel()
	regCmd := exec.CommandContext(regCtx, agentBinary, regArgs...) //nolint:gosec // operator-supplied agent
	regOut, regErr := regCmd.CombinedOutput()
	if regErr != nil {
		fmt.Fprintf(os.Stderr,
			"acpwrap: warning: auto-registering stado as %s MCP server FAILED (%v): %s\nTo register manually:\n  %s\n",
			check.Name, regErr, strings.TrimSpace(string(regOut)),
			formatRegisterDescription(check, stadoBin))
		return
	}
	fmt.Fprintf(os.Stderr,
		"acpwrap: registered stado as %s MCP server at user scope (auto). Equivalent: %s\n",
		check.Name, formatRegisterDescription(check, stadoBin))
}

// isStadoRegisteredInConfig reads the wrapped agent's config file
// and returns whether stado is currently registered as an MCP
// server. Returns (false, nil) for "missing config file" — the
// agent might be freshly installed; falling through to register is
// the right behaviour. Returns (false, err) only for parse errors
// where we've seen content but couldn't make sense of it.
func isStadoRegisteredInConfig(check agentMCPCheck) (bool, error) {
	path := check.ConfigPath
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("home dir: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-controlled config path, fixed per agent
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	switch check.ConfigFormat {
	case "json":
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return false, fmt.Errorf("parse json %s: %w", path, err)
		}
		return check.ConfigStadoPresent(parsed), nil
	case "toml":
		// Pass raw bytes; tomlMcpServersHasStado does line-level
		// header detection without a TOML library.
		return check.ConfigStadoPresent(raw), nil
	default:
		return false, fmt.Errorf("unsupported config format %q", check.ConfigFormat)
	}
}

func substituteStadoBin(args []string, stadoBin string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "{STADO_BIN}", stadoBin)
	}
	return out
}

// lookupAgentCheck normalises the binary path to a basename + lower-
// case key and returns the matching check rule.
func lookupAgentCheck(agentBinary string) (agentMCPCheck, bool) {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(agentBinary)))
	if base == "" {
		return agentMCPCheck{}, false
	}
	check, ok := agentChecksByBinary[base]
	return check, ok
}

// formatRegisterDescription substitutes {STADO_BIN} in the
// human-readable register description, used in stderr logs only —
// the actual command runs via RegisterArgs (argv form, no shell
// escaping).
func formatRegisterDescription(check agentMCPCheck, stadoBin string) string {
	return strings.ReplaceAll(check.RegisterDescription, "{STADO_BIN}", stadoBin)
}

