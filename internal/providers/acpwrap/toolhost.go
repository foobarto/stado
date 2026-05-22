package acpwrap

// ACP tool-host: translates inbound `fs/*` and `terminal/*` JSON-RPC
// requests from a wrapped agent into stado tool registry calls. The
// wrapped agent's calls are treated as untrusted — every invocation
// flows through the supplied tool.Host (which carries capability
// checks, permission rules, audit emission). See EP-0032 D7.
//
// Phase B.1 (this commit): fs/read_text_file + fs/write_text_file.
// Phase B.2: terminal/* lifecycle.
//
// Spec reference: https://agentclientprotocol.com/protocol/file-system

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/pkg/tool"
)

// toolhostDebug returns true when STADO_ACP_TOOLHOST_DEBUG is set
// to any non-empty value. Used to log dispatched method names to
// stderr during smoke tests / dogfood — the `stado mcp-server` side
// already emits otel spans, but those go to the configured exporter
// (off by default in dev). Stderr lets a smoke-test operator see in
// real time what the wrapped agent is calling without setting up
// telemetry.
func toolhostDebug() bool {
	return os.Getenv("STADO_ACP_TOOLHOST_DEBUG") != ""
}

// ToolHostConfig configures the inbound-request handler that
// translates ACP method calls into stado tool invocations.
//
// All fields are required when phase B is enabled (`tools = "stado"`
// in the provider config); a nil ReadTool / WriteTool / Host produces
// CodeInternalError responses on every call.
type ToolHostConfig struct {
	// ReadTool runs the read implementation for fs/read_text_file.
	ReadTool tool.Tool

	// WriteTool runs the write implementation for fs/write_text_file.
	WriteTool tool.Tool

	// Host is the tool.Host both tools are invoked with — this is
	// where the permission/sandbox/audit stack hooks in.
	Host tool.Host

	// MCPServerName is the name stado mounts itself under in
	// session/new.mcpServers (see BuildStadoMCPMount — "stado").
	// session/request_permission auto-approve is scoped to tool calls
	// that resolve to THIS server (or stado's advertised fs/*
	// methods); everything else — the wrapped agent's own built-in
	// tools, third-party MCP servers — is denied rather than blindly
	// approved. Empty falls back to the canonical "stado" so the
	// scope is never accidentally wide-open. See #051.
	MCPServerName string
}

// stadoMCPServerName is the canonical name stado mounts itself under
// (matches BuildStadoMCPMount). Used as the default scope for
// session/request_permission auto-approve when ToolHostConfig.
// MCPServerName is unset. See #051.
const stadoMCPServerName = "stado"

// BuildRequestHandler returns an acp.RequestHandler that dispatches
// canonical ACP fs/* and terminal/* methods to the configured tools.
// Methods not implemented in this revision return CodeMethodNotFound
// so spec-compliant agents can fall back to their built-ins (or
// surface the gap to the user) cleanly.
func BuildRequestHandler(cfg ToolHostConfig) acp.RequestHandler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if toolhostDebug() {
			fmt.Fprintf(os.Stderr, "[acpwrap toolhost] dispatch %s params=%s\n", method, string(params))
		}
		switch method {
		case "fs/read_text_file":
			return handleReadTextFile(ctx, cfg, params)
		case "fs/write_text_file":
			return handleWriteTextFile(ctx, cfg, params)
		case "session/request_permission":
			return handleRequestPermission(cfg, params)
		default:
			return nil, &acp.RPCError{
				Code:    acp.CodeMethodNotFound,
				Message: "acpwrap toolhost: method not implemented: " + method,
			}
		}
	}
}

// acpPermissionParams matches session/request_permission params:
// `{sessionId, toolCall: {toolCallId, ...}, options: [{optionId, name, kind}]}`.
type acpPermissionParams struct {
	SessionID string                `json:"sessionId"`
	ToolCall  map[string]any        `json:"toolCall"`
	Options   []acpPermissionOption `json:"options"`
}

type acpPermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// acpPermissionResult is the canonical response wrapping the chosen
// option in an outer "outcome" object — `{outcome: {outcome:
// "selected", optionId: "..."}}`. The double-nested "outcome" is
// intentional per the spec: the inner discriminates between
// "selected" (option chosen) and "cancelled" (prompt-turn
// interrupted).
type acpPermissionResult struct {
	Outcome acpPermissionOutcome `json:"outcome"`
}

type acpPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// handleRequestPermission auto-approves ONLY tool calls that resolve
// to stado's own mounted MCP server or stado's advertised fs/*
// methods — those route through stado's Executor (audit + sandbox),
// so auto-approve preserves rather than removes the safety boundary.
// For anything else (the wrapped agent's built-in bash/fs/web tools,
// or a third-party MCP server it also mounts), it returns "cancelled"
// (deny) instead of blindly picking the most-permissive allow_*
// option. See #051.
//
// Rationale for the previous blanket-allow being wrong: `tools =
// "stado"` does NOT disable the wrapped agent's built-in tools (see
// provider.go Config.Tools doc). A prompt-injected or compromised
// agent could request permission for its own shell/fs/network tools
// and have stado rubber-stamp a persistent allow_always grant,
// removing the human approval boundary for operations stado never
// audits. Scoping auto-approve to stado-routed calls keeps the
// always-on automation for the integration the operator opted into
// while denying everything outside that trust boundary.
//
// Falls back to "cancelled" if the agent didn't supply any allow-
// shaped options (unusual but handled rather than panic).
func handleRequestPermission(cfg ToolHostConfig, raw json.RawMessage) (any, error) {
	var p acpPermissionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInvalidParams,
			Message: "session/request_permission: " + err.Error(),
		}
	}

	// #051: scope-check. Only auto-approve calls that target stado's
	// own MCP server / fs methods. Out-of-scope calls (the wrapped
	// agent's built-ins, other MCP servers) are denied here rather
	// than being handed the most-permissive allow option.
	serverName := cfg.MCPServerName
	if strings.TrimSpace(serverName) == "" {
		serverName = stadoMCPServerName
	}
	if !toolCallIsStadoRouted(p.ToolCall, serverName) {
		if toolhostDebug() {
			fmt.Fprintf(os.Stderr,
				"[acpwrap toolhost] denying out-of-scope permission request (not a %q-routed tool): %+v\n",
				serverName, p.ToolCall)
		}
		return acpPermissionResult{Outcome: acpPermissionOutcome{Outcome: "cancelled"}}, nil
	}

	// Pick by kind in priority order — agents may name options
	// differently (proceed_always_server vs allow_always vs
	// allow-always), so kind is the stable selector.
	preferenceOrder := []string{"allow_always", "allow_once"}
	for _, kind := range preferenceOrder {
		for _, opt := range p.Options {
			if opt.Kind == kind {
				return acpPermissionResult{Outcome: acpPermissionOutcome{
					Outcome: "selected", OptionID: opt.OptionID,
				}}, nil
			}
		}
	}
	// Some agents (gemini-cli observed) include a non-standard
	// "allow_always_server" kind in addition to the canonical
	// "allow_always". Match either via prefix.
	for _, opt := range p.Options {
		if strings.HasPrefix(opt.Kind, "allow_") {
			return acpPermissionResult{Outcome: acpPermissionOutcome{
				Outcome: "selected", OptionID: opt.OptionID,
			}}, nil
		}
	}

	// No allow option offered — return cancelled rather than
	// guessing.
	return acpPermissionResult{Outcome: acpPermissionOutcome{Outcome: "cancelled"}}, nil
}

// toolCallIsStadoRouted reports whether an ACP session/request_
// permission `toolCall` object identifies a tool that routes through
// stado — i.e. a tool exposed by stado's mounted MCP server
// (serverName) or one of stado's advertised fs/* methods. See #051.
//
// ACP does not standardise a single "tool id" field on the toolCall
// object; agents put the identifier in different places (toolCallId,
// title, a nested rawInput.name, or a kind). MCP-derived tools are
// namespaced by their server name across the surveyed agents
// (gemini/claude/codex/opencode) as `<server>__<tool>` or
// `mcp__<server>__<tool>`. We therefore look for the stado server
// name as a namespaced token, plus the canonical fs/* method names
// stado advertises in clientCapabilities. Detection is intentionally
// conservative: an unrecognised toolCall is treated as out-of-scope
// (denied) rather than approved.
func toolCallIsStadoRouted(toolCall map[string]any, serverName string) bool {
	if len(toolCall) == 0 {
		return false
	}
	// Namespaced-token markers for the stado MCP server. The double
	// underscore is the de-facto MCP tool-namespacing separator;
	// requiring it (rather than a bare substring) avoids matching an
	// unrelated tool whose name merely contains "stado".
	markers := []string{
		serverName + "__",       // <server>__<tool>
		"mcp__" + serverName,    // mcp__<server>__<tool>
		"mcp_" + serverName,     // some agents use single underscores
		"__" + serverName + "_", // suffix/middle namespacing variants
	}
	// Walk the candidate identifier-bearing fields ONLY. We do NOT
	// scan the whole serialized toolCall: rawInput carries
	// attacker-controlled content (e.g. a bash `command` string), so a
	// blob scan would let a malicious built-in call smuggle a
	// `stado__` token into its arguments and falsely gain scope. By
	// restricting to identifier fields, an unrecognised schema fails
	// closed (denied) rather than open. See #051.
	for _, key := range []string{"toolCallId", "title", "name", "toolName", "tool"} {
		if s, ok := toolCall[key].(string); ok {
			if matchStadoMarker(s, serverName, markers) {
				return true
			}
		}
	}
	// Bounded check for an explicit server-name field some agents
	// attach to MCP-derived tool calls (e.g. {serverName:"stado"} or
	// {mcpServer:"stado"}). Exact-match the server name — not a marker
	// substring — since this is a dedicated identity field.
	for _, key := range []string{"serverName", "mcpServer", "server"} {
		if s, ok := toolCall[key].(string); ok && s == serverName {
			return true
		}
	}
	return false
}

// matchStadoMarker returns true when s is an fs/* method stado
// advertises, or carries a stado MCP namespace marker.
func matchStadoMarker(s, serverName string, markers []string) bool {
	// stado's advertised fs/* methods route through the toolhost
	// dispatcher (handleReadTextFile / handleWriteTextFile) and thus
	// through stado's Executor/Host — in-scope.
	if s == "fs/read_text_file" || s == "fs/write_text_file" {
		return true
	}
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// acpReadParams matches the canonical ACP fs/read_text_file shape:
// `{sessionId, path, line?, limit?}`. line is 1-based; limit is a
// line count from line.
type acpReadParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type acpReadResult struct {
	Content string `json:"content"`
}

func handleReadTextFile(ctx context.Context, cfg ToolHostConfig, raw json.RawMessage) (any, error) {
	if cfg.ReadTool == nil || cfg.Host == nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: "acpwrap toolhost: ReadTool/Host not configured",
		}
	}
	var p acpReadParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInvalidParams,
			Message: "fs/read_text_file: " + err.Error(),
		}
	}
	if p.Path == "" {
		return nil, &acp.RPCError{
			Code:    acp.CodeInvalidParams,
			Message: "fs/read_text_file: path is required",
		}
	}

	// Translate ACP {line, limit} → stado {start, end} (1-indexed,
	// inclusive). line set + limit set → end = line+limit-1. line
	// set + limit unset → no end (read to EOF).
	stadoArgs := struct {
		Path  string `json:"path"`
		Start *int   `json:"start,omitempty"`
		End   *int   `json:"end,omitempty"`
	}{Path: p.Path}
	if p.Line != nil {
		start := *p.Line
		stadoArgs.Start = &start
		if p.Limit != nil {
			end := *p.Line + *p.Limit - 1
			stadoArgs.End = &end
		}
	}
	argsRaw, err := json.Marshal(stadoArgs)
	if err != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: "fs/read_text_file: marshal stado args: " + err.Error(),
		}
	}

	res, runErr := cfg.ReadTool.Run(ctx, argsRaw, cfg.Host)
	if runErr != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: fmt.Sprintf("fs/read_text_file: %s", runErr.Error()),
		}
	}
	if res.Error != "" {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: "fs/read_text_file: " + res.Error,
		}
	}
	return acpReadResult{Content: res.Content}, nil
}

// acpWriteParams matches the canonical ACP fs/write_text_file shape:
// `{sessionId, path, content}`. Spec: response is `null` on success;
// the client MUST create the file if it doesn't exist (the stado
// WriteTool already does so).
type acpWriteParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

func handleWriteTextFile(ctx context.Context, cfg ToolHostConfig, raw json.RawMessage) (any, error) {
	if cfg.WriteTool == nil || cfg.Host == nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: "acpwrap toolhost: WriteTool/Host not configured",
		}
	}
	var p acpWriteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInvalidParams,
			Message: "fs/write_text_file: " + err.Error(),
		}
	}
	if p.Path == "" {
		return nil, &acp.RPCError{
			Code:    acp.CodeInvalidParams,
			Message: "fs/write_text_file: path is required",
		}
	}

	stadoArgs := struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{Path: p.Path, Content: p.Content}
	argsRaw, err := json.Marshal(stadoArgs)
	if err != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: "fs/write_text_file: marshal stado args: " + err.Error(),
		}
	}

	res, runErr := cfg.WriteTool.Run(ctx, argsRaw, cfg.Host)
	if runErr != nil {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: fmt.Sprintf("fs/write_text_file: %s", runErr.Error()),
		}
	}
	if res.Error != "" {
		return nil, &acp.RPCError{
			Code:    acp.CodeInternalError,
			Message: "fs/write_text_file: " + res.Error,
		}
	}
	// ACP spec: result is null on success.
	return nil, nil
}
