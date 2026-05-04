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
		},
		{
			Name:        "codex",
			DisplayName: "Codex CLI (OpenAI)",
			Binaries:    []string{"codex"},
			ConfigPaths: []string{".codex", ".config/codex"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://github.com/openai/codex",
			VersionArg:  "--version",
		},
		{
			Name:        "opencode",
			DisplayName: "opencode (OSS ACP-compat agent)",
			Binaries:    []string{"opencode"},
			ConfigPaths: []string{".local/share/opencode", ".config/opencode"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://opencode.ai/",
			VersionArg:  "--version",
		},
		{
			Name:        "zed",
			DisplayName: "Zed editor",
			Binaries:    []string{"zed", "zed-cli"},
			ConfigPaths: []string{".config/zed"},
			Protocols:   []Protocol{ProtocolACP},
			HelpURL:     "https://zed.dev/",
			VersionArg:  "--version",
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
