package main

// `stado mcp-server` — expose stado's bundled tool registry as an MCP
// server over stdio. Other MCP clients (Claude Desktop, Cursor, any
// MCP-aware agent) can then call stado's read/grep/bash/webfetch as
// if they were first-party tools.
//
// Scope is deliberately small: tools-only, no resources, no prompts,
// no sampling. The host is auto-approve at the policy layer — the
// MCP client is assumed to be the authorization boundary — but every
// call routes through the shared Executor (so otel audit spans emit
// per call) and the host exposes sandbox.Detect() as the bash Runner
// (so shell commands run inside bwrap / sandbox-exec / equivalent
// confinement, matching the TUI/run paths).
//
// Phase B of EP-0032 spawns this binary as the wrapped agent's
// `mcpServers` mount; the audit + sandbox upgrade here is what gives
// ACP-wrapped sessions per-tool-call audit granularity (D7).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/tool"
)

var mcpServerCmd = &cobra.Command{
	Use:   "mcp-server",
	Short: "Expose stado's bundled tools as an MCP server over stdio",
	Long: "Run stado as an MCP v1 server on stdio. Every bundled stado tool\n" +
		"(read, grep, ripgrep, ast-grep, bash, webfetch, tasks, file ops, LSP-find)\n" +
		"is registered with the server and callable via MCP tools/call.\n\n" +
		"[tools].enabled / [tools].disabled in config.toml trim the exposed set\n" +
		"same as the TUI and run paths — an MCP client only sees the tools\n" +
		"stado is currently configured to offer.\n\n" +
		"Tool execution uses an auto-approve host rooted at the process cwd.\n" +
		"The MCP client is responsible for authorization; stado trusts the\n" +
		"caller in mcp-server mode. For human-in-the-loop approval, use the\n" +
		"TUI or `stado run` without --tools.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("mcp-server: config: %w", err)
		}
		return withTelemetry(cmd.Context(), cfg, func(context.Context) error {
			reg := runtime.BuildDefaultRegistry(cfg)
			reg.Register(tasktool.Tool{Path: tasks.StorePath(cfg.StateDir())})
			runtime.ApplyToolFilter(reg, cfg)

			srv := server.NewMCPServer("stado", stadoVersion())
			runner := sandbox.Detect()
			host := stadoMCPHost{workdir: mustCwd(), runner: runner}

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
			return server.ServeStdio(srv)
		})
	},
}

// registerStadoTool wires a stado tool into the MCP server. Input
// schema is the stado tool's Schema() verbatim; handler unmarshals
// the MCP request args, delegates to executor.Run (which emits an
// otel audit span and applies the sandbox runner via the host),
// and packages the Result as MCP content.
//
// Going through Executor.Run rather than t.Run directly is the audit
// surface every other stado entry point uses (TUI, `stado run`,
// plugin-run with --with-tool-host). MCP clients now show up in the
// audit trail with `tool.name` + `tool.outcome` + `tool.duration_ms`
// like any other caller. Bash specifically also picks up sandbox
// confinement because the host exposes Runner().
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

func init() {
	rootCmd.AddCommand(mcpServerCmd)
}
