package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
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

	opts := runtime.AgentLoopOptions{
		Provider:             s.Provider,
		Model:                s.Cfg.Defaults.Model,
		Messages:             localMsgs,
		MaxTurns:             10,
		Thinking:             s.Cfg.Agent.Thinking,
		ThinkingBudgetTokens: s.Cfg.Agent.ThinkingBudgetTokens,
		System:               sysPrompt,
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
			opts.Executor = exec
		}
	}

	text, msgs, err := runtime.AgentLoop(pctx, opts)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
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
	soft := s.Cfg.Context.SoftThreshold
	if soft <= 0 {
		soft = 0.70
	}
	fraction := float64(inputTokens) / float64(cap)
	if fraction < soft {
		return
	}
	level := "soft"
	hard := s.Cfg.Context.HardThreshold
	if hard <= 0 {
		hard = 0.90
	}
	if fraction >= hard {
		level = "hard"
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
