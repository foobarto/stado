package main

// `stado mcp-server` — expose stado's bundled tool registry as an MCP
// server over stdio. Other MCP clients (Claude Desktop, Cursor, any
// MCP-aware agent) can then call stado's read/grep/bash/webfetch as
// if they were first-party tools.
//
// Scope is deliberately small: tools-only, no resources, no prompts,
// no sampling. The host is auto-approve at the policy layer — the
// MCP client is assumed to be the authorization boundary — and every
// call routes through the shared Executor so otel audit spans emit
// per call.
//
// Sandboxing posture (post EP-no-internal-tools Step 4 + 2026-05-09
// runner-plumb-through):
//
// The legacy in-process bash tool consulted Runner() on the host to
// confine itself with bubblewrap / sandbox-exec. That tool is gone —
// `bash` is now the wasm tool `shell.exec`, which routes through
// stado_exec. The original 2026-05-09 review caught a comment here
// claiming Runner() still confined bash; it didn't, because the wasm
// shell never set the `sandbox` field in its stado_exec request.
//
// Now: the host implements tool.SandboxPolicyProvider (see method
// below) which returns a default protective policy from
// pluginRuntime.NewDefaultSandboxPolicy. host_proc.go:resolveSandboxPolicy
// applies this default when the wasm guest doesn't supply its own.
// Net effect: bash invocations through MCP run under bwrap /
// sandbox-exec by default (PID + uid namespace isolation).
//
// Host-as-ceiling (post-2026-05-09 redesign): when a guest supplies
// its own `sandbox` field, the resolver intersects host and guest —
// the guest can only TIGHTEN host policy, never weaken it. A plugin
// that sets `unsandboxed: true` to bypass confinement is IGNORED
// when a host default exists; operators who want to allow opt-outs
// remove the host default explicitly. See
// host_proc.go:resolveSandboxPolicy + intersectPolicies for the
// per-field rules. Operators wanting tighter defaults patch
// pluginRuntime.NewDefaultSandboxPolicy or wire a config-driven
// override.
//
// Phase B of EP-0032 spawns this binary as the wrapped agent's
// `mcpServers` mount; the audit upgrade here is what gives ACP-wrapped
// sessions per-tool-call audit granularity (D7).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools/llmtool"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

var mcpServerCmd = &cobra.Command{
	Use:   "mcp-server",
	Short: "Expose stado's bundled tools as an MCP server over stdio",
	Long: "Run stado as an MCP v1 server on stdio. Every bundled stado tool\n" +
		"(read, grep, ripgrep, ast-grep, bash, webfetch, tasks, file ops, LSP-find)\n" +
		"is registered with the server and callable via MCP tools/call.\n\n" +
		"WIRE FORMAT: newline-delimited JSON-RPC 2.0 (one JSON message per line\n" +
		"on stdin / stdout). MCP v1 stdio uses newline framing — NOT the\n" +
		"Content-Length headers that LSP uses. Sending a Content-Length\n" +
		"prelude will fail at the JSON-RPC parser; reconfigure your client\n" +
		"to send raw newline-framed messages instead.\n\n" +
		"[tools].enabled / [tools].disabled in config.toml trim the exposed set\n" +
		"same as the TUI and run paths — an MCP client only sees the tools\n" +
		"stado is currently configured to offer.\n\n" +
		"Tool execution uses an auto-approve host rooted at the process cwd.\n" +
		"The MCP client is responsible for authorization; stado trusts the\n" +
		"caller in mcp-server mode. For human-in-the-loop approval, use the\n" +
		"TUI or `stado run` without --tools.\n\n" +
		"Smoke test: echo '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' \\\n" +
		"  | stado mcp-server | head -1",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("mcp-server: config: %w", err)
		}
		return withTelemetry(cmd.Context(), cfg, func(context.Context) error {
			// Shared composition with the agent loop / CLI: bundled +
			// installed plugin tools + tasks + MCP-attached + wasm
			// migration + overrides + filter. Step 0.5 of EP-no-internal-
			// tools — pre-converge the MCP server skipped MCP attach,
			// wasm migration, and tool overrides.
			reg, err := runtime.BuildRegistryWithPlugins(cfg)
			if err != nil {
				return fmt.Errorf("mcp-server: registry: %w", err)
			}
			// llm.invoke — MCP-server-only tool exposing stado's
			// configured provider with persona selection. Deliberately
			// not in BuildRegistryWithPlugins because it doesn't belong
			// on the agent registry (model uses stado_agent_* for
			// sub-LLM delegation, not a model-facing llm.invoke tool).
			reg.Register(llmtool.Tool{
				Provider:       func() (agent.Provider, error) { return tui.BuildProvider(cfg) },
				DefaultModel:   cfg.Defaults.Model,
				DefaultPersona: mcpServerPersona,
				CWD:            mustCwd(),
				ConfigDir:      config.ConfigDir(),
			})

			srv := server.NewMCPServer("stado", stadoVersion())
			runner := sandbox.Detect()
			host := stadoMCPHost{
				workdir: mustCwd(),
				runner:  runner,
				// Server-lifetime PTY manager — shell.spawn → shell.read
				// across MCP calls share state. Reaped on server exit.
				pty: pty.NewManager(),
			}
			defer host.pty.CloseAll()

			// Executor wraps each tool.Run with otel audit spans +
			// latency metrics — same path the TUI and `stado run`
			// take. No git Session: mcp-server calls are single-shot
			// without a stadogit conversation to commit against.
			executor := &tools.Executor{
				Registry: reg,
				Session:  nil,
				Runner:   runner,
				Metrics:  telemetry.Metrics{},
				Agent:    "stado-mcp-server",
				Model:    cfg.Defaults.Model,
				ReadLog:  nil, // single-shot calls don't dedup
			}

			for _, t := range reg.All() {
				registerStadoTool(srv, t, host, executor)
			}
			fmt.Fprintf(os.Stderr, "stado mcp-server: serving %d tool(s) on stdio (sandbox: %s)\n",
				len(reg.All()), runner.Name())
			// stdin-is-a-TTY advisory: the operator probably typed
			// `stado mcp-server` directly (no client connecting). Without
			// this, the server appears to hang waiting for newline-framed
			// JSON-RPC that's never coming. B6.
			if isatty.IsTerminal(os.Stdin.Fd()) {
				fmt.Fprintln(os.Stderr,
					"stado mcp-server: stdin is a terminal — the server expects newline-delimited\n"+
						"JSON-RPC from a client process. Pipe a client into stdin, or run a\n"+
						"smoke test like:\n"+
						"  echo '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' | stado mcp-server | head -1\n"+
						"Press Ctrl+D to exit.")
			}
			return server.ServeStdio(srv)
		})
	},
}

// registerStadoTool wires a stado tool into the MCP server. Input
// schema is the stado tool's Schema() verbatim; handler unmarshals
// the MCP request args, delegates to executor.Run (which emits an
// otel audit span and emits the runner name as a span attribute),
// and packages the Result as MCP content.
//
// Going through Executor.Run rather than t.Run directly is the audit
// surface every other stado entry point uses (TUI, `stado run`,
// plugin-run with --with-tool-host). MCP clients now show up in the
// audit trail with `tool.name` + `tool.outcome` + `tool.duration_ms`
// like any other caller. The wasm shell path picks up bwrap /
// sandbox-exec confinement via the host's DefaultSandboxPolicy.
func registerStadoTool(srv *server.MCPServer, t tool.Tool, host stadoMCPHost, executor *tools.Executor) {
	mcpTool := mcp.NewToolWithRawSchema(t.Name(), t.Description(), rawSchema(t.Schema()))
	name := t.Name()
	srv.AddTool(mcpTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		argsJSON, err := json.Marshal(req.GetArguments())
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encoding args", err), nil
		}
		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := executor.Run(ctx, name, argsJSON, host)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("tool run", err), nil
		}
		if res.Error != "" {
			return mcp.NewToolResultError(res.Error), nil
		}
		return mcp.NewToolResultText(res.Content), nil
	})
}

// rawSchema marshals a stado schema map into the JSON bytes MCP
// expects. Falls back to a permissive "any object" schema when the
// tool's map can't be marshalled (shouldn't happen for bundled
// tools — they all hand-write their schemas).
func rawSchema(m map[string]any) json.RawMessage {
	if m == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	body, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado mcp-server: bad schema: %v\n", err)
		return json.RawMessage(`{"type":"object"}`)
	}
	return body
}

// stadoMCPHost is an auto-approve Host with a fixed workdir, no
// read-dedup log, and an exposed sandbox runner. Single-shot MCP
// calls don't have a running session to dedup against. The Runner()
// method makes the bash tool sandbox-aware (it does an interface
// type-assert to find this method); without Runner() exposed, bash
// would run unsandboxed even on hosts where bwrap/sandbox-exec is
// available — silent and bad.
type stadoMCPHost struct {
	workdir string
	runner  sandbox.Runner
	// pty is a server-lifetime PTY manager shared across every tool
	// dispatch so shell.spawn → shell.attach / read / write succeed
	// across calls. Without this each bundled-plugin runtime would
	// instantiate its own pty.NewManager() and the second call would
	// fail with "session not found." Bug-fix per operator report.
	pty *pty.Manager
}

func (h stadoMCPHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h stadoMCPHost) Workdir() string        { return h.workdir }
func (h stadoMCPHost) Runner() sandbox.Runner { return h.runner }
func (h stadoMCPHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (h stadoMCPHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

// PTYManager implements pkg/tool.PTYProvider — bundled shell.* /
// pty.* tools reuse the server-lifetime manager via this hook so
// session ids survive across MCP calls.
func (h stadoMCPHost) PTYManager() any { return h.pty }

// DefaultSandboxPolicy implements tool.SandboxPolicyProvider. Plugins
// calling stado_exec / stado_proc_spawn from MCP without supplying
// their own `sandbox` field get the host-default protective policy
// (PID + uid namespace isolation via bwrap / sandbox-exec, plus the
// FSRead/FSWrite/Net values defined by NewDefaultSandboxPolicy).
//
// Plugins supplying an explicit `sandbox` field intersect with this
// default — they can only TIGHTEN host policy, never weaken it. The
// resolver lives at host_proc.go:resolveSandboxPolicy + intersectPolicies.
func (h stadoMCPHost) DefaultSandboxPolicy() any {
	return pluginRuntime.NewDefaultSandboxPolicy(h.workdir)
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// stadoVersion returns a short version string for the MCP server
// identification. Matches what `stado verify` reports; kept as a
// helper so there's one source of truth if the format ever moves.
func stadoVersion() string {
	bi := collectBuildInfo()
	if bi.Version != "" {
		return bi.Version
	}
	return "dev"
}

var mcpServerPersona string

func init() {
	mcpServerCmd.Flags().StringVar(&mcpServerPersona, "persona", "",
		"Persona that supplies the operating manual when stado's LLM is "+
			"invoked through MCP (via the `llm.invoke` tool, agent.spawn, "+
			"etc.). Empty = [defaults].persona from config, or bundled "+
			"default. Per-call args still override.")
	rootCmd.AddCommand(mcpServerCmd)
}
