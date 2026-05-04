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
}

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
		default:
			return nil, &acp.RPCError{
				Code:    acp.CodeMethodNotFound,
				Message: "acpwrap toolhost: method not implemented: " + method,
			}
		}
	}
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
