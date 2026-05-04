// Package mcpwrap implements an agent.Provider that wraps a
// coding-agent CLI exposed as an MCP server (e.g.
// `codex mcp-server`).
//
// Why MCP and not ACP: not every coding-agent CLI exposes a
// stdio ACP-agent mode. codex (as of v0.125.0) only exposes itself
// via MCP — `codex mcp-server` advertises two tools, `codex` (start
// new session) and `codex-reply` (continue thread by id). Stado
// wraps that surface as an agent.Provider so users can drive codex
// from the same TUI/run code path used for Anthropic, gemini --acp,
// opencode acp, etc.
//
// Lifecycle: one provider instance owns one wrapped MCP server
// subprocess plus one persistent thread id (re-used by codex-reply
// across turns within a session). First StreamTurn lazy-spawns the
// subprocess + sends `initialize`. Each subsequent StreamTurn maps
// the last user message to a single MCP `tools/call`:
//   - Empty thread id (first turn) → call `codex` with {prompt}.
//     Result `{threadId, content}` becomes the assistant turn.
//     threadId is captured for later turns.
//   - Non-empty thread id (continuation) → call `codex-reply` with
//     {threadId, prompt}.
//
// Output streaming: codex's MCP tools return whole-turn results
// (no progressive token streaming). The provider emits the entire
// content as a single EvTextDelta then EvDone — accurate to what
// codex actually exposes. Future codex revisions that add
// `notifications/progress` MCP messages would let us emit
// progressive deltas; today we mirror the synchronous behaviour.
//
// Tool registry: stado does not advertise its own tools to codex
// here (no analogue of acpwrap's `tools = "stado"` opt-in). codex
// runs under its own sandbox + tool stack, configurable via the
// optional `[mcp.providers.<name>] config_overrides` table.
package mcpwrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/foobarto/stado/pkg/agent"
)

// Config is the provider build-time config. Mirrors fields under
// `[mcp.providers.<name>]` in config.toml.
type Config struct {
	// Name is the canonical provider id (e.g. "codex-mcp").
	Name string

	// Binary is the absolute path to or PATH-resolvable name of the
	// wrapped agent's executable. Required.
	Binary string

	// Args is the argv passed to Binary to launch its MCP server
	// mode (e.g. ["mcp-server"] for codex).
	Args []string

	// CallTool is the MCP tool name to invoke for the FIRST turn
	// in a session (no threadId yet). For codex, "codex".
	CallTool string

	// ContinueTool is the MCP tool name to invoke for SUBSEQUENT
	// turns when a threadId has been captured. For codex,
	// "codex-reply". When empty, every turn calls CallTool with no
	// threadId — appropriate for stateless wrapped agents.
	ContinueTool string

	// PromptArgKey is the input-schema field name carrying the user
	// prompt. Defaults to "prompt".
	PromptArgKey string

	// ThreadIDArgKey is the input-schema field name carrying the
	// thread id on continuation calls. Defaults to "threadId".
	ThreadIDArgKey string

	// ContentResultKey is the output-schema field name to extract
	// from the tool's structured result as the assistant text.
	// Defaults to "content".
	ContentResultKey string

	// ThreadIDResultKey is the output-schema field name to extract
	// as the captured thread id. Defaults to "threadId".
	ThreadIDResultKey string

	// CallToolOverrides is a static map of additional fields to
	// merge into every tools/call's `arguments` object (for codex
	// this is where things like `model`, `sandbox`, `approval-policy`
	// land if the operator wants to pin them). Operator-supplied,
	// passed through verbatim.
	CallToolOverrides map[string]any
}

// Provider is the agent.Provider implementation.
type Provider struct {
	cfg Config

	mu       sync.Mutex
	client   *client.Client
	threadID string
}

// New constructs a Provider. The wrapped subprocess is NOT spawned
// here — it lazy-launches on the first StreamTurn. Required-field
// validation happens at New() time so config errors surface at boot.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, errors.New("mcpwrap: Binary is required")
	}
	if strings.TrimSpace(cfg.CallTool) == "" {
		return nil, errors.New("mcpwrap: CallTool is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "mcp"
	}
	cfg.PromptArgKey = nonEmpty(cfg.PromptArgKey, "prompt")
	cfg.ThreadIDArgKey = nonEmpty(cfg.ThreadIDArgKey, "threadId")
	cfg.ContentResultKey = nonEmpty(cfg.ContentResultKey, "content")
	cfg.ThreadIDResultKey = nonEmpty(cfg.ThreadIDResultKey, "threadId")
	return &Provider{cfg: cfg}, nil
}

// nonEmpty returns the first non-empty of {a, b}.
func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func (p *Provider) Name() string { return p.cfg.Name }

func (p *Provider) Capabilities() agent.Capabilities {
	// Tools live inside the wrapped agent's session; stado has no
	// visibility into them at this layer. Match acpwrap defaults.
	return agent.Capabilities{
		MaxParallelToolCalls: 0,
		MaxContextTokens:     0,
	}
}

// ensureLaunched lazy-spawns the wrapped MCP server and runs the
// initialize handshake. Subsequent calls are no-ops.
func (p *Provider) ensureLaunched(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return nil
	}

	// Stdio MCP client: spawns the subprocess, pipes stdin/stdout,
	// runs JSON-RPC. Uses mark3labs/mcp-go's stdio transport which
	// matches what stado's existing `internal/mcp/client.go` and
	// `cmd/stado/mcp_server.go` use — same wire format.
	c, err := client.NewStdioMCPClient(p.cfg.Binary, os.Environ(), p.cfg.Args...)
	if err != nil {
		return fmt.Errorf("mcpwrap: spawn %s: %w", p.cfg.Binary, err)
	}

	// MCP initialize. 30s timeout — wrapped CLIs do non-trivial work
	// at startup (codex loads config, sets up auth) but anything past
	// 30s indicates a real problem.
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = c.Initialize(initCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "stado-mcpwrap",
				Version: "0.0.0-dev",
			},
		},
	})
	if err != nil {
		_ = c.Close()
		return fmt.Errorf("mcpwrap: initialize %s: %w", p.cfg.Binary, err)
	}

	p.client = c
	return nil
}

// Close shuts down the wrapped subprocess. Safe to call multiple
// times; idempotent.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return nil
	}
	err := p.client.Close()
	p.client = nil
	return err
}

// StreamTurn fulfills agent.Provider. Maps the LAST user message to
// a single MCP tools/call. First-turn-in-session uses CallTool;
// continuation uses ContinueTool with the captured thread id.
//
// The wrapped tool returns a synchronous full-response result; we
// emit that as one EvTextDelta plus EvDone. No progressive
// streaming today (codex's MCP server doesn't emit
// notifications/progress for the codex/codex-reply tools).
func (p *Provider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	if err := p.ensureLaunched(ctx); err != nil {
		return nil, err
	}

	prompt, err := lastUserText(req.Messages)
	if err != nil {
		return nil, err
	}

	out := make(chan agent.Event, 4)

	go func() {
		defer close(out)

		args := map[string]any{p.cfg.PromptArgKey: prompt}
		// Merge static overrides (model, sandbox, etc.). Operator-
		// supplied entries don't override the prompt key — that
		// would be a foot-gun.
		for k, v := range p.cfg.CallToolOverrides {
			if k == p.cfg.PromptArgKey {
				continue
			}
			args[k] = v
		}

		p.mu.Lock()
		toolName := p.cfg.CallTool
		if p.threadID != "" && p.cfg.ContinueTool != "" {
			toolName = p.cfg.ContinueTool
			args[p.cfg.ThreadIDArgKey] = p.threadID
		}
		c := p.client
		p.mu.Unlock()

		callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		res, callErr := c.CallTool(callCtx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      toolName,
				Arguments: args,
			},
		})
		if callErr != nil {
			out <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("mcpwrap: %s call: %w", toolName, callErr)}
			return
		}
		if res.IsError {
			out <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("mcpwrap: %s reported error: %s", toolName, extractErrText(res))}
			return
		}

		text, threadID := extractContentAndThread(res, p.cfg.ContentResultKey, p.cfg.ThreadIDResultKey)
		if threadID != "" {
			p.mu.Lock()
			p.threadID = threadID
			p.mu.Unlock()
		}

		if text != "" {
			out <- agent.Event{Kind: agent.EvTextDelta, Text: text}
		}
		out <- agent.Event{Kind: agent.EvDone}
	}()

	return out, nil
}

// lastUserText pulls the last user-role message's text out of the
// accumulated history. Wrapped MCP agents hold their own session
// state server-side via threadId; we only send the latest user
// turn.
func lastUserText(msgs []agent.Message) (string, error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != agent.RoleUser {
			continue
		}
		var sb strings.Builder
		for _, c := range m.Content {
			if c.Text != nil {
				sb.WriteString(c.Text.Text)
			}
		}
		text := sb.String()
		if text != "" {
			return text, nil
		}
	}
	return "", errors.New("mcpwrap: no user message in request")
}

// extractContentAndThread parses the tool's structured-content
// result into (assistant text, thread id). Tries structuredContent
// first (canonical for tools with outputSchema like codex's), falls
// back to the unstructured Content array if no structured payload.
func extractContentAndThread(res *mcp.CallToolResult, contentKey, threadKey string) (string, string) {
	// Prefer structuredContent — codex's `codex` and `codex-reply`
	// tools both declare outputSchema.{threadId, content} so the
	// MCP server emits this branch.
	if res.StructuredContent != nil {
		// StructuredContent is documented as `any` per mcp-go; the
		// canonical shape is map[string]any. Marshal/unmarshal to
		// get a normalised view.
		raw, err := json.Marshal(res.StructuredContent)
		if err == nil {
			var parsed map[string]any
			if json.Unmarshal(raw, &parsed) == nil {
				text, _ := parsed[contentKey].(string)
				thread, _ := parsed[threadKey].(string)
				if text != "" || thread != "" {
					return text, thread
				}
			}
		}
	}
	// Fallback: concatenate any text-typed Content entries. Loses
	// the threadId (no place to put it in unstructured form), so
	// continuation calls would degrade to fresh sessions.
	var sb strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String(), ""
}

// extractErrText pulls a human-readable error message from
// IsError-flagged results. MCP errors come back with the message
// inside Content rather than a separate field.
func extractErrText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok && t.Text != "" {
			return t.Text
		}
	}
	return "tool reported error with no message"
}
