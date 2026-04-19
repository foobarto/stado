// Package headless is stado's editor-neutral JSON-RPC daemon surface.
//
// Reuses the line-delimited JSON-RPC transport from internal/acp so one
// implementation covers both the Zed-specific ACP server and this general
// one. Method set differs: headless uses dot-cased method names
// (session.new, tools.list, …) and is intended for scripting + editor
// integrations that aren't Zed.
package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// Server is the headless JSON-RPC daemon.
type Server struct {
	Cfg      *config.Config
	Provider agent.Provider

	conn     *acp.Conn
	mu       sync.Mutex
	sessions map[string]*hSession

	// Background-plugin state. See plugins.go. Populated on Serve()
	// entry from cfg.Plugins.Background and torn down on exit.
	bgRuntime *pluginRuntime.Runtime
	bgPlugins []*pluginRuntime.BackgroundPlugin
}

type hSession struct {
	id              string
	messages        []agent.Message
	cancel          context.CancelFunc
	workdir         string
	gitSess         *stadogit.Session // lazy, set by ensureGitSession
	lastInputTokens int               // most recent input-token observation
}

func NewServer(cfg *config.Config, prov agent.Provider) *Server {
	return &Server{Cfg: cfg, Provider: prov, sessions: map[string]*hSession{}}
}

// Serve runs the loop on r/w until the peer disconnects. Loads
// cfg.Plugins.Background plugins before dispatch starts; tears them
// down on exit.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.conn = acp.NewConn(r, w)
	s.loadBackgroundPlugins(ctx)
	defer s.closeBackgroundPlugins(context.Background())
	return s.conn.Serve(ctx, s.dispatch)
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session.new":
		return s.sessionNew()
	case "session.prompt":
		return s.sessionPrompt(ctx, params)
	case "session.list":
		return s.sessionList(), nil
	case "session.cancel":
		return s.sessionCancel(params)
	case "session.delete":
		return s.sessionDelete(params)
	case "session.compact":
		return s.sessionCompact(ctx, params)
	case "tools.list":
		return s.toolsList(), nil
	case "providers.list":
		return map[string]any{
			"available": []string{"anthropic", "openai", "google", "ollama", "llamacpp", "vllm"},
			"current":   s.Cfg.Defaults.Provider,
		}, nil
	case "plugin.list":
		return s.pluginList(), nil
	case "plugin.run":
		return s.pluginRun(ctx, params)
	case "shutdown":
		s.conn.Close()
		return struct{}{}, nil
	}
	return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "unknown method: " + method}
}

type sessionNewResult struct {
	SessionID string `json:"sessionId"`
	Workdir   string `json:"workdir"`
}

func (s *Server) sessionNew() (any, error) {
	cwd, _ := os.Getwd()
	id := fmt.Sprintf("h-%d", len(s.sessions)+1)
	s.mu.Lock()
	s.sessions[id] = &hSession{id: id, workdir: cwd}
	s.mu.Unlock()
	return sessionNewResult{SessionID: id, Workdir: cwd}, nil
}

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
	sess.messages = append(sess.messages, agent.Text(agent.RoleUser, p.Prompt))

	pctx, cancel := context.WithCancel(ctx)
	sess.cancel = cancel
	defer func() { sess.cancel = nil }()

	opts := runtime.AgentLoopOptions{
		Provider:             s.Provider,
		Model:                s.Cfg.Defaults.Model,
		Messages:             sess.messages,
		MaxTurns:             10,
		Thinking:             s.Cfg.Agent.Thinking,
		ThinkingBudgetTokens: s.Cfg.Agent.ThinkingBudgetTokens,
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
			// headless emits session.update{kind:"context_warning"} when
			// soft threshold is crossed. Fired on Usage events (end of
			// turn) so clients see it before the next prompt.
			if (ev.Kind == agent.EvUsage || ev.Kind == agent.EvDone) && ev.Usage != nil {
				sess.lastInputTokens = ev.Usage.InputTokens
				s.maybeEmitContextWarning(p.SessionID, ev.Usage.InputTokens)
			}
		},
	}
	if p.Tools {
		s.ensureGitSession(sess)
		if sess.gitSess != nil {
			opts.Executor = runtime.BuildExecutor(sess.gitSess, s.Cfg, "stado-headless")
		}
	}

	text, msgs, err := runtime.AgentLoop(pctx, opts)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	sess.messages = msgs

	// Turn-boundary tick for background plugins. Runs sequentially
	// after the reply is assembled — clients get their result first,
	// then any plugin_fork / compaction notifications arrive as
	// separate session.update messages.
	s.tickBackgroundPlugins(pctx, sess)

	return sessionPromptResult{Text: text}, nil
}

type sessionListItem struct {
	SessionID string `json:"sessionId"`
	Turns     int    `json:"turns"`
	Workdir   string `json:"workdir"`
}

func (s *Server) sessionList() []sessionListItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sessionListItem, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sessionListItem{
			SessionID: sess.id,
			Turns:     countAssistantTurns(sess.messages),
			Workdir:   sess.workdir,
		})
	}
	return out
}

func countAssistantTurns(msgs []agent.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == agent.RoleAssistant {
			n++
		}
	}
	return n
}

type toolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Class       string `json:"class"`
}

func (s *Server) toolsList() []toolInfo {
	reg := runtime.BuildDefaultRegistry()
	all := reg.All()
	out := make([]toolInfo, 0, len(all))
	for _, t := range all {
		cls := reg.ClassOf(t.Name()).String()
		out = append(out, toolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			Class:       cls,
		})
	}
	return out
}

// Suppress unused import of filepath if future changes remove it.
var _ = filepath.Clean

// --- session.cancel / delete / compact ---

type sessionIDParam struct {
	SessionID string `json:"sessionId"`
}

// sessionCancel interrupts an in-flight session.prompt. No-op (success)
// when the session has no active stream — the cancel func is nil until
// a prompt is running.
func (s *Server) sessionCancel(raw json.RawMessage) (any, error) {
	var p sessionIDParam
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown sessionId"}
	}
	if sess.cancel != nil {
		sess.cancel()
	}
	return struct {
		Cancelled bool `json:"cancelled"`
	}{Cancelled: sess.cancel != nil}, nil
}

// sessionDelete drops a session from the in-memory map. No sidecar
// cleanup — that's `stado session delete <id>`'s job; the headless
// surface only sees the in-flight sessions created via session.new.
func (s *Server) sessionDelete(raw json.RawMessage) (any, error) {
	var p sessionIDParam
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[p.SessionID]; !ok {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown sessionId"}
	}
	delete(s.sessions, p.SessionID)
	return struct{}{}, nil
}

type sessionCompactResult struct {
	Summary      string `json:"summary"`
	PriorTurns   int    `json:"priorTurns"`
	PostTurns    int    `json:"postTurns"`
}

// sessionCompact summarises the session's conversation history via the
// configured provider and replaces the in-memory msgs with the
// compacted form. Unlike the TUI flow there's no interactive preview
// step — headless clients implement their own confirmation UX on top
// of the returned summary. Call session.prompt next to continue from
// the compacted state.
func (s *Server) sessionCompact(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sessionIDParam
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
	if len(sess.messages) == 0 {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "session has no messages to compact"}
	}

	summary, err := compact.Summarise(ctx, s.Provider, s.Cfg.Defaults.Model, sess.messages)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}

	s.mu.Lock()
	prior := len(sess.messages)
	sess.messages = compact.ReplaceMessages(summary)
	post := len(sess.messages)
	s.mu.Unlock()

	return sessionCompactResult{Summary: summary, PriorTurns: prior, PostTurns: post}, nil
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
