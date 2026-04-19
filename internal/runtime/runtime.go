// Package runtime is stado's UI-independent core: session lifecycle, tool
// executor wiring, and the headless agent loop. Both the TUI and the
// `stado run` headless surface compose this.
//
// PLAN.md §9.1 calls this "internal/core/runtime.go"; kept as "runtime" so the
// CLI import path reads naturally (internal/runtime.AgentLoop).
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/astgrep"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/lspfind"
	"github.com/foobarto/stado/internal/tools/readctx"
	"github.com/foobarto/stado/internal/tools/rg"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// OpenSession creates a new session + sidecar rooted at cwd's repo.
// Non-fatal callers can swallow the error and carry on without state.
//
// Loads (or creates on first use) the agent signing key and attaches it to
// the session so every trace/tree commit carries an Ed25519 signature.
func OpenSession(cfg *config.Config, cwd string) (*stadogit.Session, error) {
	userRepo := FindRepoRoot(cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		return nil, err
	}
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), uuid.New().String(), plumbing.ZeroHash)
	if err != nil {
		return nil, err
	}

	priv, err := audit.LoadOrCreateKey(SigningKeyPath(cfg))
	if err == nil {
		sess.Signer = audit.NewSigner(priv)
	}
	// Signer is optional — unsigned commits still work; audit verify will
	// flag them.

	// Drop a pid file so `stado agents list` / `stado agents kill` can find
	// this process. Best-effort: ignore write errors (worktree might be
	// read-only or similar).
	pidPath := filepath.Join(sess.WorktreePath, ".stado-pid")
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)

	return sess, nil
}

// SigningKeyPath returns the path to stado's agent signing key.
func SigningKeyPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir(), "keys", audit.KeyFileName)
}

// FindRepoRoot walks up from start looking for a .git dir; falls back to start.
func FindRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// BuildDefaultRegistry returns a Registry preloaded with stado's bundled tools
// (bash, fs, webfetch). Separate from Executor so callers can add/remove tools
// before constructing the Executor.
func BuildDefaultRegistry() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(fs.ReadTool{})
	r.Register(fs.WriteTool{})
	r.Register(fs.EditTool{})
	r.Register(fs.GlobTool{})
	r.Register(fs.GrepTool{})
	r.Register(bash.BashTool{Timeout: 60 * time.Second})
	r.Register(webfetch.WebFetchTool{})
	r.Register(rg.Tool{})
	r.Register(astgrep.Tool{})
	r.Register(readctx.Tool{})
	r.Register(&lspfind.FindDefinition{})
	return r
}

// BuildExecutor wires the tool registry + session + sandbox runner.
//
// Also loads any MCP servers from config and registers their tools. Failed
// MCP connections are logged to stderr, not fatal — stado should boot
// without them if the endpoint is down.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string) *tools.Executor {
	reg := BuildDefaultRegistry()

	if len(cfg.MCP.Servers) > 0 {
		if err := attachMCP(reg, cfg.MCP.Servers); err != nil {
			fmt.Fprintf(os.Stderr, "stado: MCP setup: %v\n", err)
		}
	}

	return &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Agent:    agentName,
		Model:    cfg.Defaults.Model,
	}
}

// attachMCP is defined in mcp_glue.go — kept in a separate file so pulling
// the MCP SDK in is a single-file diff and easier to #ifdef out on airgap
// builds later.

// ToolDefs renders the registry as []agent.ToolDef for a TurnRequest.
func ToolDefs(reg *tools.Registry) []agent.ToolDef {
	if reg == nil {
		return nil
	}
	all := reg.All()
	out := make([]agent.ToolDef, 0, len(all))
	for _, t := range all {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

// AgentLoopOptions parameterises a headless agent loop. Callers typically
// pre-build Executor (which owns the registry + session) and feed initial
// messages; the loop streams turn → tool calls → tool exec → next turn until
// no tool calls remain, or MaxTurns is hit.
type AgentLoopOptions struct {
	Provider agent.Provider
	Executor *tools.Executor
	Model    string
	Messages []agent.Message

	MaxTurns int // default 20

	// OnEvent receives every provider event before any accumulation happens.
	// Useful for stdout streaming in `stado run`.
	OnEvent func(agent.Event)

	// OnTurnComplete fires after a turn streams (text + tool-calls). Callers
	// can log or inspect without intervening.
	OnTurnComplete func(text string, toolCalls []agent.ToolUseBlock)

	// Host implements tool.Host during tool execution. Defaults to an
	// auto-approve host using Session.WorktreePath as workdir.
	Host tool.Host
}

// AgentLoop runs the headless multi-turn loop. Returns the final assistant
// text (concatenated across turns) and the final accumulated message
// history. Error is returned unchanged from the provider or executor.
func AgentLoop(ctx context.Context, opts AgentLoopOptions) (string, []agent.Message, error) {
	if opts.Provider == nil {
		return "", opts.Messages, errors.New("runtime: provider required")
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 20
	}
	if opts.Host == nil {
		workdir := ""
		if opts.Executor != nil && opts.Executor.Session != nil {
			workdir = opts.Executor.Session.WorktreePath
		}
		opts.Host = autoApproveHost{workdir: workdir}
	}

	msgs := opts.Messages
	var finalText string

	for turn := 0; turn < opts.MaxTurns; turn++ {
		req := agent.TurnRequest{
			Model:    opts.Model,
			Messages: msgs,
		}
		if opts.Executor != nil {
			req.Tools = ToolDefs(opts.Executor.Registry)
		}
		ch, err := opts.Provider.StreamTurn(ctx, req)
		if err != nil {
			return finalText, msgs, fmt.Errorf("stream: %w", err)
		}

		text, calls, err := collectTurn(ch, opts.OnEvent)
		if err != nil {
			return finalText, msgs, err
		}
		if opts.OnTurnComplete != nil {
			opts.OnTurnComplete(text, calls)
		}

		// Flush assistant turn (text + tool_uses) into history.
		var asst []agent.Block
		if text != "" {
			asst = append(asst, agent.Block{Text: &agent.TextBlock{Text: text}})
		}
		for i := range calls {
			tc := calls[i]
			asst = append(asst, agent.Block{ToolUse: &tc})
		}
		if len(asst) > 0 {
			msgs = append(msgs, agent.Message{Role: agent.RoleAssistant, Content: asst})
		}
		finalText += text

		if len(calls) == 0 {
			return finalText, msgs, nil
		}
		if opts.Executor == nil {
			return finalText, msgs, errors.New("runtime: tool calls requested but executor is nil")
		}

		// Execute tool calls, build role=tool message.
		var results []agent.Block
		for _, c := range calls {
			res, runErr := opts.Executor.Run(ctx, c.Name, c.Input, opts.Host)
			content := res.Content
			isErr := res.Error != ""
			if runErr != nil {
				content = runErr.Error()
				isErr = true
			} else if isErr {
				content = res.Error
			}
			results = append(results, agent.Block{ToolResult: &agent.ToolResultBlock{
				ToolUseID: c.ID,
				Content:   content,
				IsError:   isErr,
			}})
		}
		msgs = append(msgs, agent.Message{Role: agent.RoleTool, Content: results})
	}
	return finalText, msgs, fmt.Errorf("runtime: exceeded %d turns", opts.MaxTurns)
}

// collectTurn drains an event stream into (assistant_text, tool_calls).
func collectTurn(ch <-chan agent.Event, onEvent func(agent.Event)) (string, []agent.ToolUseBlock, error) {
	var text string
	var calls []agent.ToolUseBlock
	for ev := range ch {
		if onEvent != nil {
			onEvent(ev)
		}
		switch ev.Kind {
		case agent.EvTextDelta:
			text += ev.Text
		case agent.EvToolCallEnd:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case agent.EvError:
			return text, calls, ev.Err
		}
	}
	return text, calls, nil
}

type autoApproveHost struct{ workdir string }

func (h autoApproveHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h autoApproveHost) Workdir() string { return h.workdir }
