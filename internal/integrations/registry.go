// Package integrations enumerates external coding-agent CLIs stado can
// interoperate with — primarily via ACP (the Zed-introduced
// Agent Client Protocol), with MCP as a secondary fallback.
//
// First-party scope (what this package KNOWS about, regardless of
// whether they're installed on the current host):
//
//   - claude       — Anthropic's Claude Code CLI. ACP server.
//   - gemini       — Google's Gemini CLI. ACP server.
//   - codex        — OpenAI's Codex CLI. ACP server.
//   - opencode     — open-source ACP-compat agent CLI.
//   - zed          — Zed editor. Acts as ACP host (server-side).
//   - aider        — popular OSS agent. MCP server (no ACP yet).
//
// New integrations land here by adding a row to the slice in
// KnownIntegrations(); detection logic is in detect.go.
package integrations

// Protocol is the wire interop protocol an external agent supports
// (or, in stado's case, will be configured to speak).
type Protocol string

const (
	ProtocolACP Protocol = "acp" // Agent Client Protocol (Zed)
	ProtocolMCP Protocol = "mcp" // Model Context Protocol (Anthropic)
)

// Integration is one entry in the known-agent registry. UI/CLI
// surfaces should iterate KnownIntegrations() rather than
// hard-coding names so adding a new agent is a one-place change.
type Integration struct {
	// Name is the canonical short id used in config + CLI flags.
	Name string

	// DisplayName is the human-facing label.
	DisplayName string

	// Binaries lists candidate executable names to look for on PATH.
	// First match wins. Common to list both `<name>` and `<name>.exe`
	// for Windows-friendly detection (we still only test exec.LookPath).
	Binaries []string

	// WellKnownPaths lists absolute paths (with `~/` prefix-expansion)
	// where this agent's binary commonly lives even when not on PATH.
	// Hermes installs at ~/.hermes/hermes-agent/hermes; opencode at
	// ~/.opencode/bin/opencode; etc. Detection checks these BEFORE
	// PATH so a broken shim on PATH (we've seen Python wrappers with
	// ModuleNotFoundError) doesn't shadow a working install.
	WellKnownPaths []string

	// ConfigPaths lists XDG-style relative paths under HOME / XDG_CONFIG_HOME
	// that, if present, indicate the agent is installed and configured.
	// Used for detection signal even when the binary isn't on PATH (e.g.
	// installed via npm into a non-PATH dir but config still written).
	ConfigPaths []string

	// Protocols lists the wire protocols this agent can act as a server
	// for. Order = preferred (ACP > MCP).
	Protocols []Protocol

	// HelpURL is a short hint for where to install / read more about this
	// agent. Optional.
	HelpURL string

	// VersionArg is the CLI flag that prints the agent's version (e.g.
	// "--version", "version"). Used for the version probe in Detect()
	// when the binary is found.
	VersionArg string

	// ACPArgs is the argv passed to the binary to launch its stdio
	// ACP-agent server mode (e.g. ["--acp"] for gemini, ["acp"] for
	// opencode and hermes). Empty when the agent doesn't expose a
	// stdio ACP-agent mode (codex, claude — they only act as ACP
	// agents through other transports). Used by the auto-provider
	// fallback in `buildProviderByName` when a `<name>-acp` provider
	// is requested without a matching `[acp.providers.<name>-acp]`
	// config entry.
	ACPArgs []string

	// MCPWrapTools is the (CallTool, ContinueTool) pair used by the
	// mcpwrap provider to drive this agent via its MCP-server mode
	// (e.g. codex exposes a `codex` tool for new sessions and a
	// `codex-reply` tool for continuations). Empty when the agent
	// can't be wrapped via MCP. Used by the auto-provider fallback
	// for `<name>-mcp` provider names.
	MCPWrapTools [2]string

	// MCPWrapServerArgs is the argv to launch the agent's MCP server
	// (e.g. ["mcp-server"] for codex). Required when MCPWrapTools is
	// non-empty.
	MCPWrapServerArgs []string
}

// KnownIntegrations returns the bundled integration catalogue in
// display order. Source of truth for `stado integrations`,
// `stado doctor`, and any future "spawn this agent over ACP" picker.
func KnownIntegrations() []Integration {
	return []Integration{
		{
			Name:        "claude",
			DisplayName: "Claude Code (Anthropic)",
			Binaries:    []string{"claude"},
			ConfigPaths: []string{".claude", ".config/claude"},
			Protocols:   []Protocol{ProtocolACP, ProtocolMCP},
			HelpURL:     "https://docs.claude.com/en/docs/claude-code",
			VersionArg:  "--version",
		},
		{
			Name:        "gemini",
			DisplayName: "Gemini CLI (Google)",
			Binaries:    []string{"gemini"},
			ConfigPaths: []string{".gemini", ".config/gemini"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://github.com/google-gemini/gemini-cli",
			VersionArg:  "--version",
			ACPArgs:     []string{"--acp"},
		},
		{
			Name:              "codex",
			DisplayName:       "Codex CLI (OpenAI)",
			Binaries:          []string{"codex"},
			ConfigPaths:       []string{".codex", ".config/codex"},
			Protocols:         []Protocol{ProtocolACP, ProtocolMCP}, // ACP listed for parity but unimplemented; MCP is the wrap path
			HelpURL:           "https://github.com/openai/codex",
			VersionArg:        "--version",
			// codex doesn't expose a stdio ACP-agent mode; ACPArgs left empty
			// so the auto-fallback won't synthesize a broken acp provider.
			MCPWrapTools:      [2]string{"codex", "codex-reply"},
			MCPWrapServerArgs: []string{"mcp-server"},
		},
		{
			Name:        "opencode",
			DisplayName: "opencode (OSS ACP-compat agent)",
			Binaries:    []string{"opencode"},
			ConfigPaths: []string{".local/share/opencode", ".config/opencode"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://opencode.ai/",
			VersionArg:  "--version",
			ACPArgs:     []string{"acp"},
		},
		{
			Name:        "zed",
			DisplayName: "Zed editor",
			Binaries:    []string{"zed", "zed-cli"},
			ConfigPaths: []string{".config/zed"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://zed.dev/",
			VersionArg:  "--version",
			// Zed is an editor — no stdio ACP-agent mode to wrap.
		},
		{
			Name:        "hermes",
			DisplayName: "Hermes Agent",
			Binaries:    []string{"hermes"},
			// Prefer the venv binary — the root ~/.hermes/hermes-agent/hermes
			// is a system-python launcher that fails with
			// `ModuleNotFoundError: hermes_cli`. The venv's hermes has the
			// agent_client_protocol extra installed and ACP works.
			WellKnownPaths: []string{
				"~/.hermes/hermes-agent/venv/bin/hermes",
				"~/.hermes/hermes-agent/hermes",
			},
			ConfigPaths: []string{".hermes", ".config/hermes"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://hermes.run/",
			VersionArg:  "--version",
			ACPArgs:     []string{"acp"},
		},
		{
			Name:        "aider",
			DisplayName: "aider (OSS pair-programming agent)",
			Binaries:    []string{"aider"},
			ConfigPaths: []string{".aider", ".config/aider"},
			Protocols:   []Protocol{ProtocolMCP},
			HelpURL:     "https://aider.chat/",
			VersionArg:  "--version",
		},
	}
}
