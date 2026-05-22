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
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
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
	//
	// SECURITY (#048): these overrides are advisory only — stado
	// passes them to the external agent but cannot enforce them. The
	// wrapped agent runs its own fs/shell/network tool stack with the
	// privileges of the launched subprocess; a value like
	// sandbox="read-only" is honored only if the agent chooses to.
	CallToolOverrides map[string]any

	// Cwd is the working directory the wrapped MCP server subprocess
	// runs in. #048: set this to the session's worktree path so the
	// wrapped agent's filesystem tools default to the sidecar rather
	// than stado's inherited process cwd (the real checkout). Empty =
	// inherit stado's cwd (legacy behavior; logs a startup warning so
	// the operator knows the isolation boundary is not in place).
	Cwd string

	// Env, when set, REPLACES the scrubbed-safelist environment passed
	// to the subprocess (it is not appended to os.Environ()). #048:
	// the wrapped subprocess no longer inherits the full parent
	// environment by default — only a small safelist (HOME, PATH,
	// USER, XDG_*, TERM) plus these explicit entries — so inherited
	// secrets (API keys, tokens) are not handed to an external agent
	// stado cannot audit. Each entry is "KEY=value".
	Env []string
}

// envSafelist is the set of environment variables passed through to
// the wrapped MCP server subprocess. #048: we deliberately do NOT
// inherit the full os.Environ() — an external agent stado can't audit
// should not receive arbitrary inherited secrets (cloud creds, API
// tokens, etc.). Only variables the subprocess plausibly needs to
// locate config / a shell / a terminal are forwarded; the operator
// adds anything else explicitly via Config.Env.
var envSafelist = []string{
	"HOME",
	"PATH",
	"USER",
	"LOGNAME",
	"SHELL",
	"TERM",
	"LANG",
	"LC_ALL",
	"TMPDIR",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_CACHE_HOME",
}

// scrubbedEnv builds the subprocess environment: the safelisted
// entries pulled from the current process, with extra appended/
// overriding. #048.
func scrubbedEnv(extra []string) []string {
	out := make([]string, 0, len(envSafelist)+len(extra))
	for _, key := range envSafelist {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	out = append(out, extra...)
	return out
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

	// #048: do NOT inherit the full parent environment. Pass a
	// scrubbed safelist (+ operator-supplied Config.Env) so an
	// external agent stado can't audit doesn't receive arbitrary
	// inherited secrets. The CommandFunc below also sets the
	// subprocess working directory to Cwd (the session worktree, when
	// wired) instead of stado's inherited process cwd.
	env := scrubbedEnv(p.cfg.Env)

	if strings.TrimSpace(p.cfg.Cwd) == "" {
		// Visibility: with no Cwd the subprocess runs in stado's cwd
		// (typically the real checkout), so the wrapped agent's fs
		// tools operate outside the sidecar boundary. #048.
		fmt.Fprintf(os.Stderr,
			"mcpwrap: warning: %s launched without a worktree Cwd — wrapped agent runs in stado's cwd, outside the sidecar/audit boundary (#048)\n",
			p.cfg.Name)
	}

	// cmdFunc builds the subprocess so we control Dir + Env. mcp-go's
	// default path appends env to os.Environ(); WithCommandFunc lets
	// us replace the environment outright and pin the working dir.
	cmdFunc := func(ctx context.Context, command string, cmdEnv []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, command, args...) // #nosec G204 — operator-supplied agent binary is the point of this provider
		cmd.Env = cmdEnv
		if dir := strings.TrimSpace(p.cfg.Cwd); dir != "" {
			cmd.Dir = dir
		}
		return cmd, nil
	}

	// Stdio MCP client: spawns the subprocess, pipes stdin/stdout,
	// runs JSON-RPC. Uses mark3labs/mcp-go's stdio transport which
	// matches what stado's existing `internal/mcp/client.go` and
	// `cmd/stado/mcp_server.go` use — same wire format.
	c, err := client.NewStdioMCPClientWithOptions(
		p.cfg.Binary, env, p.cfg.Args,
		transport.WithCommandFunc(cmdFunc),
	)
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
