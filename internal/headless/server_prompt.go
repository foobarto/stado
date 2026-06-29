package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/harness"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

type sessionPromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
	Tools     bool   `json:"tools"`
}

type sessionPromptResult struct {
	Text string `json:"text"`
}

func (s *Server) sessionPrompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sessionPromptParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown sessionId"}
	}
	if s.Provider == nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "no provider configured"}
	}
	sess.mu.Lock()
	if sess.busy {
		sess.mu.Unlock()
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "session already has an active operation"}
	}
	sess.busy = true
	sess.messages = append(sess.messages, agent.Text(agent.RoleUser, p.Prompt))
	workdir := sess.workdir

	pctx, cancel := context.WithCancel(ctx)
	sess.cancel = cancel
	defer func() {
		sess.mu.Lock()
		sess.cancel = nil
		sess.busy = false
		sess.mu.Unlock()
	}()

	// Project instructions resolved from the session's workdir, not
	// the process cwd — a headless client may hold several sessions
	// rooted at different repos. Silent on miss; warn on read error.
	sysPrompt := ""
	if workdir != "" {
		if res, err := instructions.Load(workdir); err != nil {
			_ = s.conn.Notify("session.update", map[string]any{
				"sessionId": p.SessionID,
				"kind":      "system",
				"text":      fmt.Sprintf("instructions: %v", err),
			})
		} else {
			sysPrompt = res.Content
		}
	}
	// EP-0030: security harness — prepend in security mode, same as `stado run`
	// (autonomous surface; previously the config knob was ignored here).
	harnessMode := ""
	if s.Cfg != nil {
		harnessMode = s.Cfg.Harness.Mode
	}
	sysPrompt = harness.Prepend(sysPrompt, workdir, harnessMode)

	var localMsgs []agent.Message
	if sess.messages != nil {
		localMsgs = make([]agent.Message, len(sess.messages))
		copy(localMsgs, sess.messages)
	}
	sess.mu.Unlock()
	hookRunner := hooks.Runner{
		PostTurnCmd: s.Cfg.Hooks.PostTurn,
		Disabled:    hooks.DisabledByToolConfig(s.Cfg),
	}
	// F1: scriptable deny/mutate lifecycle hooks (Lua), wired into both
	// the agent loop and (below) the executor.
	lifecycleHooks := hooks.BuildLifecycleRunner(s.Cfg)

	// EP-0045: effective skill catalog (cwd ∪ persona) so the model-facing
	// listing + skills__load work on the headless surface, matching `stado
	// run`. Non-fatal on load error.
	effectiveSkills, skErr := runtime.EffectiveSkills(workdir, sess.persona)
	if skErr != nil {
		_ = s.conn.Notify("session.update", map[string]any{
			"sessionId": p.SessionID,
			"kind":      "system",
			"text":      fmt.Sprintf("skills: %v", skErr),
		})
	}

	opts := runtime.AgentLoopOptions{
		Provider: s.Provider,
		Config:   s.Cfg,
		Model:    s.Cfg.Defaults.Model,
		Messages: localMsgs,
		MaxTurns: 10,
		Skills:   effectiveSkills,
		// Autonomous surface: confine bash/exec by default (Model A,
		// decision 2026-06-13) — matches mcp-server / acp. The auto-created
		// host returns this from tool.SandboxPolicyProvider; the wasm guest
		// can only tighten it. Closes the gap where headless bash ran
		// unconfined.
		DefaultSandboxPolicy: pluginRuntime.NewDefaultSandboxPolicy(workdir),
		Persona:              sess.persona,
		Hooks:                lifecycleHooks,
		Thinking:             s.Cfg.Agent.Thinking,
		ThinkingBudgetTokens: s.Cfg.Agent.ThinkingBudgetTokens,
		System:               sysPrompt,
		SystemTemplate:       s.Cfg.Agent.SystemPromptTemplate,
		MemoryContext:        s.memoryPromptContext(pctx, workdir, p.SessionID, p.Prompt),
		OnEvent: func(ev agent.Event) {
			if ev.Kind == agent.EvTextDelta && ev.Text != "" {
				_ = s.conn.Notify("session.update", map[string]any{
					"sessionId": p.SessionID,
					"kind":      "text",
					"text":      ev.Text,
				})
			}
			if ev.Kind == agent.EvToolCallEnd && ev.ToolCall != nil {
				_ = s.conn.Notify("session.update", map[string]any{
					"sessionId": p.SessionID,
					"kind":      "tool_call",
					"name":      ev.ToolCall.Name,
					"input":     string(ev.ToolCall.Input),
				})
			}
			// Threshold notification — DESIGN §"Token accounting" 11.2.5:
			// headless emits session.update{kind:"context_warning",level:*}
			// on completed turns at or above the configured threshold.
			// Fired on Usage events (end of turn) so clients see it before
			// the next prompt.
			if (ev.Kind == agent.EvUsage || ev.Kind == agent.EvDone) && ev.Usage != nil {
				sess.mu.Lock()
				sess.lastInputTokens = ev.Usage.InputTokens
				sess.mu.Unlock()
				s.maybeEmitContextWarning(p.SessionID, ev.Usage.InputTokens)
			}
		},
		OnTurnComplete: func(turnIndex int, text string, _ []agent.ToolUseBlock, usage agent.Usage, duration time.Duration) {
			hookRunner.FirePostTurn(pctx, hooks.NewPostTurnPayload(turnIndex, usage, text, duration))
		},
		OnSubagentEvent: func(ev runtime.SubagentEvent) {
			s.emitSubagentUpdate(p.SessionID, ev)
		},
	}

	if p.Tools {
		s.ensureGitSession(sess)
		sess.mu.Lock()
		gs := sess.gitSess
		sess.mu.Unlock()
		if gs != nil {
			exec, err := runtime.BuildExecutor(gs, s.Cfg, "stado-headless")
			if err != nil {
				return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
			}
			// F1: same lifecycle runner drives the tool-side seam.
			exec.Hooks = lifecycleHooks
			opts.Executor = exec
			// Rebuild the default sandbox policy from the SIDECAR worktree, not
			// the real checkout (Codex #5). The policy was constructed above
			// with sess.workdir (= os.Getwd(), the user's real repo) before the
			// git session existed; toSandboxPolicy prefers policy.CWD and the
			// bwrap runner binds that CWD read-write, so a headless shell.exec
			// would write straight into the user's checkout, bypassing the
			// sidecar/land isolation. The executor's tool host already uses
			// gs.WorktreePath — point the sandbox CWD at the same place.
			opts.DefaultSandboxPolicy = pluginRuntime.NewDefaultSandboxPolicy(sandboxPolicyWorkdir(workdir, gs.WorktreePath))
		}
	}

	text, msgs, err := runtime.AgentLoop(pctx, opts)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}

	sess.mu.Lock()
	gs := sess.gitSess
	persistedViewLen := sess.persistedViewLen
	sess.mu.Unlock()
	if gs != nil {
		nextPersisted, err := runtime.AppendMessagesFrom(gs.WorktreePath, msgs, persistedViewLen)
		sess.mu.Lock()
		sess.persistedViewLen = nextPersisted
		sess.mu.Unlock()
		if err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
		}
	}

	sess.mu.Lock()
	sess.messages = msgs
	sess.mu.Unlock()

	// Turn-boundary tick for background plugins. Runs sequentially
	// after the reply is assembled — clients get their result first,
	// then any plugin_fork / compaction notifications arrive as
	// separate session.update messages.
	s.tickBackgroundPlugins(pctx, sess)

	return sessionPromptResult{Text: text}, nil
}

// sandboxPolicyWorkdir picks the directory the default sandbox policy is pinned
// to. When a git session is active its sidecar worktree wins over the session's
// real-checkout workdir, so a headless shell.exec writes into the sidecar (which
// `land` later applies), not the user's repository directly (Codex #5). Falls
// back to the real workdir only when there is no sidecar worktree.
func sandboxPolicyWorkdir(sessionWorkdir, sidecarWorktree string) string {
	if sidecarWorktree != "" {
		return sidecarWorktree
	}
	return sessionWorkdir
}

func (s *Server) emitSubagentUpdate(sessionID string, ev runtime.SubagentEvent) {
	if s == nil || s.conn == nil {
		return
	}
	payload := map[string]any{
		"sessionId":       sessionID,
		"kind":            "subagent",
		"phase":           ev.Phase,
		"status":          ev.Status,
		"role":            ev.Role,
		"mode":            ev.Mode,
		"child":           ev.ChildSession,
		"childWorktree":   ev.Worktree,
		"parentSession":   ev.ParentSession,
		"timeout_seconds": ev.TimeoutSeconds,
	}
	if ev.Error != "" {
		payload["error"] = ev.Error
	}
	if ev.ForkTree != "" {
		payload["forkTree"] = ev.ForkTree
	}
	if len(ev.ChangedFiles) > 0 {
		payload["changedFiles"] = append([]string(nil), ev.ChangedFiles...)
	}
	if len(ev.ScopeViolations) > 0 {
		payload["scopeViolations"] = append([]string(nil), ev.ScopeViolations...)
	}
	if cmd := ev.AdoptionCommand(); cmd != "" {
		payload["adoptionCommand"] = cmd
	}
	_ = s.conn.Notify("session.update", payload)
}

// maybeEmitContextWarning fires a session.update{kind:"context_warning"}
// notification when inputTokens crosses cfg.Context.SoftThreshold. Hard
// threshold is left to clients to act on — the headless surface doesn't
// block its own callers (DESIGN §"Token accounting"). No-op when
// Provider.Capabilities().MaxContextTokens is unknown.
func (s *Server) maybeEmitContextWarning(sessionID string, inputTokens int) {
	if s.Provider == nil {
		return
	}
	cap := s.Provider.Capabilities().MaxContextTokens
	if cap <= 0 || inputTokens <= 0 {
		return
	}
	// R9: the soft advisory and the hard block are INDEPENDENT knobs — 0
	// disables that one gate only (per docs). config.Load already applied the
	// default when a key was unset, so a 0 here is an explicit disable.
	// Evaluate both so soft=0 + hard>0 still emits the hard event.
	fraction := float64(inputTokens) / float64(cap)
	soft := s.Cfg.Context.SoftThreshold
	hard := s.Cfg.Context.HardThreshold
	level := ""
	if soft > 0 && fraction >= soft {
		level = "soft"
	}
	if hard > 0 && fraction >= hard {
		level = "hard"
	}
	if level == "" {
		return
	}
	_ = s.conn.Notify("session.update", map[string]any{
		"sessionId":    sessionID,
		"kind":         "context_warning",
		"level":        level,
		"fraction":     fraction,
		"input_tokens": inputTokens,
		"max_tokens":   cap,
	})
}
