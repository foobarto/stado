// Package acpwrap implements an agent.Provider that wraps an external
// ACP-speaking coding-agent CLI (gemini --acp, opencode acp, future
// zed-compatible variants) as a stado provider.
//
// Phase A of the ACP-client plan: the wrapped agent owns its own tools
// (the natural ACP shape). Stado is a chat-routing UI on top —
// session management, multi-session UI, audit boundary recording. The
// wrapped agent's intermediate tool calls are NOT visible to stado;
// only the boundary messages (user prompt → agent response) are.
//
// Lifecycle: one provider instance owns one wrapped-agent subprocess.
// First StreamTurn spawns the binary, sends `initialize`, and creates
// an ACP session. Subsequent StreamTurn calls reuse the session.
// Stop the provider via Close() to send `shutdown` and reap the
// subprocess; if Close isn't called, the subprocess gets cleaned up
// when its stdin pipe closes (i.e. when stado exits).
//
// Limitations of phase A (resolved in phase B per EP-0032):
//
//   - Stado's bundled tool registry is NOT exposed to the wrapped
//     agent. Wrapped agents use their own tool stack.
//   - Tool-call events (mid-turn) are surfaced as opaque text in the
//     stado audit log; the wrapped agent's internal tool-call
//     granularity isn't preserved.
//   - One process per provider; multi-session-per-process not
//     implemented yet.
//
// See EP-0032 for the design rationale + phase B/C plan.
package acpwrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/pkg/agent"
)

// Config is the provider build-time config. Mirrors the fields that
// land in [providers.acp.<name>] in config.toml.
type Config struct {
	// Name is the canonical provider id (e.g. "gemini-acp"). Returned
	// from agent.Provider.Name().
	Name string

	// Binary is the absolute path to or PATH-resolvable name of the
	// wrapped agent's executable.
	Binary string

	// Args is the argv passed to Binary to launch its ACP server mode
	// (e.g. ["--acp"] for gemini, ["acp"] for opencode).
	Args []string

	// CWD is the working directory the wrapped agent should report as
	// its session's cwd. Empty = stado's cwd at first-stream time.
	CWD string

	// Env adds entries to the wrapped agent's environment. Inherits
	// the parent's PATH/HOME/etc by default; explicit entries here
	// override.
	Env []string
}

// Provider is the agent.Provider implementation.
type Provider struct {
	cfg Config

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	client   *acp.Client
	session  string
	initInfo *acp.AgentInitializeResult

	// updates is the buffer between the ACP client's read goroutine
	// (which calls onUpdate from inside readLoop) and the per-turn
	// StreamTurn that fans events into agent.Event. Each new
	// StreamTurn replaces the channel atomically under mu so an
	// orphaned update from a cancelled turn doesn't leak into the
	// next one.
	updates chan json.RawMessage
}

// New constructs a Provider. The wrapped subprocess is NOT spawned
// here — it lazy-launches on the first StreamTurn so a config with
// dozens of ACP providers doesn't fork dozens of subprocesses at
// boot.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, errors.New("acpwrap: Binary is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "acp"
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) Name() string { return p.cfg.Name }

func (p *Provider) Capabilities() agent.Capabilities {
	// The agent's advertised capabilities arrive in initInfo, but
	// most aren't 1-to-1 mappable to stado's Capabilities shape
	// (which models prompt-cache breakpoints + Anthropic-style
	// thinking). For the wrapped path we report conservative
	// defaults; the real capability negotiation happens between the
	// wrapped agent and its underlying model.
	return agent.Capabilities{
		MaxParallelToolCalls: 0, // tools live inside the wrapped agent, not here
		MaxContextTokens:     0, // unknown — wrapped agent decides
	}
}

// StreamTurn fulfills agent.Provider. The first call lazy-launches
// the wrapped subprocess + initializes the ACP session; subsequent
// calls reuse them.
func (p *Provider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	if err := p.ensureLaunched(ctx); err != nil {
		return nil, err
	}

	// The agent loop calls StreamTurn with the FULL accumulated
	// message history. ACP sessions hold their own history server-
	// side, so we only need to send the LAST user message. Pull it
	// out of req.Messages.
	prompt, err := lastUserText(req.Messages)
	if err != nil {
		return nil, err
	}

	// Fresh per-turn updates channel; close-on-completion lets
	// downstream ranges terminate.
	turnUpdates := make(chan json.RawMessage, 32)
	p.mu.Lock()
	p.updates = turnUpdates
	sessionID := p.session
	client := p.client
	p.mu.Unlock()

	out := make(chan agent.Event, 32)

	go func() {
		defer close(out)
		defer func() {
			p.mu.Lock()
			if p.updates == turnUpdates {
				p.updates = nil
			}
			p.mu.Unlock()
		}()

		// Drain session/update notifications into agent.Events while
		// SessionPrompt is in flight.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-ctx.Done():
					return
				case raw, ok := <-turnUpdates:
					if !ok {
						return
					}
					if ev, emit := translateUpdate(raw); emit {
						out <- ev
					}
				}
			}
		}()

		_, err := client.SessionPrompt(ctx, sessionID, prompt)
		// SessionPrompt has returned — drain any straggler updates
		// (the agent typically sends a final "agent_message" or
		// "agent_message_chunk" right before completion).
		close(turnUpdates)
		<-done

		if err != nil {
			out <- agent.Event{Kind: agent.EvError, Err: err}
			return
		}
		out <- agent.Event{Kind: agent.EvDone, Usage: &agent.Usage{}}
	}()

	return out, nil
}

func (p *Provider) ensureLaunched(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil && p.session != "" {
		return nil
	}

	cwd := p.cfg.CWD
	if strings.TrimSpace(cwd) == "" {
		if c, err := os.Getwd(); err == nil {
			cwd = c
		}
	}

	cmd := exec.Command(p.cfg.Binary, p.cfg.Args...) // #nosec G204 — operator-supplied binary path is the whole point of this provider
	cmd.Dir = cwd
	if len(p.cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), p.cfg.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acpwrap: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acpwrap: stdout pipe: %w", err)
	}
	// Forward the agent's stderr to ours so OAuth prompts /
	// auth-required errors surface (gemini-cli prints those).
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("acpwrap: start %s: %w", p.cfg.Binary, err)
	}

	client := acp.NewClient(stdout, stdin, p.handleUpdate)

	initRes, err := client.Initialize(ctx, acp.ClientInitializeParams{
		ProtocolVersion: 1,
		ClientInfo:      &acp.ClientInfo{Name: "stado", Version: "0.27.0"},
	})
	if err != nil {
		_ = client.Close(err)
		_ = cmd.Process.Kill()
		return fmt.Errorf("acpwrap: initialize: %w", err)
	}

	sessionID, err := client.SessionNew(ctx, cwd)
	if err != nil {
		_ = client.Close(err)
		_ = cmd.Process.Kill()
		return fmt.Errorf("acpwrap: session/new: %w", err)
	}

	p.cmd = cmd
	p.stdin = stdin
	p.stdout = stdout
	p.client = client
	p.session = sessionID
	p.initInfo = initRes
	return nil
}

// handleUpdate is the SessionUpdateHandler passed to acp.NewClient.
// Runs on the client's read goroutine — must not block; we push to
// the per-turn updates channel under mu and DROP if no turn is in
// flight (orphan updates from after-cancel are noise).
func (p *Provider) handleUpdate(_ string, raw json.RawMessage) {
	p.mu.Lock()
	ch := p.updates
	p.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- raw:
	default:
		// Buffer full — drop. This is rare in practice; 32-deep
		// buffer is enough for most reasoning rates. If we see
		// drops bite real workflows we'll bump the buffer or
		// switch to an unbounded channel.
	}
}

// translateUpdate maps a single ACP session/update payload into a
// stado agent.Event. The canonical-spec update shape is:
//
//	{
//	  "sessionUpdate": "agent_message_chunk" | "agent_message" |
//	                   "tool_call" | "tool_call_update" |
//	                   "agent_thought_chunk" | "available_commands_update" |
//	                   "stop_reason",
//	  "content": {"type":"text","text":"..."}    // single block (most agents)
//	          OR [{"type":"text","text":"..."}], // array of blocks (some)
//	  ...
//	}
//
// `content` shows up as both a single object AND an array of objects
// depending on the agent + the update kind, so we extractTextBlocks
// to normalise both shapes.
func translateUpdate(raw json.RawMessage) (agent.Event, bool) {
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Content       json.RawMessage `json:"content"`
		ToolCall      *struct {
			Title string `json:"title"`
			Name  string `json:"name"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return agent.Event{}, false
	}
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_message":
		text := extractTextBlocks(u.Content)
		if text == "" {
			return agent.Event{}, false
		}
		return agent.Event{Kind: agent.EvTextDelta, Text: text, Native: raw}, true
	case "tool_call", "tool_call_update":
		// Surface tool calls as text breadcrumbs. Phase B replaces
		// this with proper EvToolCallStart / EvToolCallEnd events.
		name := ""
		if u.ToolCall != nil {
			name = u.ToolCall.Name
			if u.ToolCall.Title != "" {
				name = u.ToolCall.Title
			}
		}
		if name == "" {
			return agent.Event{}, false
		}
		return agent.Event{
			Kind:   agent.EvTextDelta,
			Text:   fmt.Sprintf("\n[tool: %s]\n", name),
			Native: raw,
		}, true
	case "agent_thought_chunk":
		text := extractTextBlocks(u.Content)
		if text == "" {
			return agent.Event{}, false
		}
		return agent.Event{Kind: agent.EvThinkingDelta, Text: text, Native: raw}, true
	}
	return agent.Event{}, false
}

// extractTextBlocks pulls every {"type":"text","text":"..."} chunk
// out of `content`, accepting either a single block or an array of
// blocks. Returns the concatenated text. Empty result for non-text
// blocks (image, audio, etc. — phase-B will route those properly).
func extractTextBlocks(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	type textBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	// Try single-object shape first.
	var single textBlock
	if err := json.Unmarshal(raw, &single); err == nil && single.Type == "text" {
		return single.Text
	}
	// Fall back to array shape.
	var arr []textBlock
	if err := json.Unmarshal(raw, &arr); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range arr {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// Close shuts the wrapped agent down via `shutdown` and reaps the
// subprocess. Idempotent. Best-effort: errors are returned but the
// subprocess is killed regardless.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdownErr := p.client.Shutdown(ctx)
	_ = p.client.Close(io.EOF)
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	p.client = nil
	p.session = ""
	return shutdownErr
}

func lastUserText(msgs []agent.Message) (string, error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != agent.RoleUser {
			continue
		}
		var b strings.Builder
		for _, blk := range msgs[i].Content {
			if blk.Text != nil {
				b.WriteString(blk.Text.Text)
			}
		}
		if b.Len() > 0 {
			return b.String(), nil
		}
	}
	return "", errors.New("acpwrap: no user message in request")
}
