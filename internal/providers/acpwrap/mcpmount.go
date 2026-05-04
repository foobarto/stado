package acpwrap

// MCP-server mount: builds the entry passed to the wrapped agent in
// `session/new.mcpServers` so the agent connects to a `stado
// mcp-server` subprocess and sees stado's full tool registry as
// MCP-callable.
//
// Spec reference (stdio transport):
// https://agentclientprotocol.com/protocol/session-setup
//
// Wire shape (stdio):
//
//	{
//	  "name":    "stado",
//	  "command": "/abs/path/to/stado",
//	  "args":    ["mcp-server"],
//	  "env":     [{"name": "...", "value": "..."}]
//	}
//
// The wrapped agent spawns the command, talks MCP over its stdio,
// and surfaces the tools it discovers via tools/list to the LLM as
// standard MCP tools. The stado mcp-server side routes every call
// through the Executor (audit span emission) with sandbox.Detect()
// applied to bash — see the prior commit (mcp-server upgrade).
//
// EP-0032 phase B (D6): MCP-mount is the C in A+C — without it,
// forcing the wrapped agent to use only stado-routed tools (via the
// agent's CLI built-in-disabling flag) leaves only fs+terminal as
// the available surface. With it, the agent has the full registry
// and the user's audit goal holds even under "stado-only" mode.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MCPServerMount describes one entry for the ACP `session/new`
// `mcpServers` array. JSON tags match the canonical Zed-spec shape
// (stdio transport — see https://agentclientprotocol.com/protocol/session-setup).
type MCPServerMount struct {
	Name    string         `json:"name"`
	Command string         `json:"command"`
	Args    []string       `json:"args"`
	Env     []MCPServerEnv `json:"env,omitempty"`
}

// MCPServerEnv is one entry of MCPServerMount.Env.
type MCPServerEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// BuildStadoMCPMount constructs the MCPServerMount entry for an
// in-process stado-as-MCP-server. The wrapped agent will spawn this
// command and discover stado's tool registry via MCP tools/list.
//
// Uses os.Executable() to find stado's own binary path so the spawn
// reuses the running version — important when developers run a
// local-built stado: the wrapped agent should mount THAT one, not
// some `stado` on $PATH that may be stale or from a different
// branch.
//
// Inherited env defaults to the running process's env, scoped to a
// safelist that's relevant for stado's config loader (XDG_*, HOME,
// PATH). Additional entries (e.g. STADO_TELEMETRY_OFF=1 to suppress
// telemetry from the spawned subprocess) can be appended via
// extraEnv.
func BuildStadoMCPMount(extraEnv []MCPServerEnv) (MCPServerMount, error) {
	exe, err := os.Executable()
	if err != nil {
		return MCPServerMount{}, fmt.Errorf("acpwrap: locate stado binary: %w", err)
	}
	// Resolve symlinks so a 'stado' symlink in $PATH doesn't get
	// passed to the wrapped agent (which may not have $PATH access
	// to resolve it).
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	mount := MCPServerMount{
		Name:    "stado",
		Command: exe,
		Args:    []string{"mcp-server"},
	}

	// Pass through stado-relevant env so the spawned mcp-server
	// loads the same config (XDG_CONFIG_HOME), state dir, etc.
	// Don't blanket-inherit os.Environ() — wrapped agent may log
	// the env list and we don't want to leak unrelated secrets.
	envSafelist := []string{
		"HOME",
		"PATH",
		"USER",
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
		"XDG_STATE_HOME",
		"XDG_CACHE_HOME",
		"STADO_CONFIG_PATH",
	}
	for _, key := range envSafelist {
		if v := os.Getenv(key); v != "" {
			mount.Env = append(mount.Env, MCPServerEnv{Name: key, Value: v})
		}
	}
	mount.Env = append(mount.Env, extraEnv...)

	return mount, nil
}

// validateMount returns an error describing any obviously-wrong
// fields. Public so tests and integration paths can sanity-check
// the produced mount before passing it to the wrapped agent (a
// malformed entry typically returns a vague spec-violation error
// from the wrapped agent's stdin parser, hard to debug after the
// fact).
func validateMount(m MCPServerMount) error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("MCPServerMount: Name is required")
	}
	if strings.TrimSpace(m.Command) == "" {
		return fmt.Errorf("MCPServerMount: Command is required")
	}
	if !filepath.IsAbs(m.Command) {
		return fmt.Errorf("MCPServerMount: Command must be absolute path (got %q)", m.Command)
	}
	for i, e := range m.Env {
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("MCPServerMount: Env[%d] missing Name", i)
		}
	}
	return nil
}
