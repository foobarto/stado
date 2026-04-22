package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// ProtocolVersion advertised in the initialize handshake. Bumping requires
// coordinated update in stado + any ACP client.
const ProtocolVersion = 1

// Server is the stado ACP server — stdin/stdout JSON-RPC, one connection.
type Server struct {
	Cfg      *config.Config
	Provider agent.Provider

	// EnableTools, if set, means session.prompt opens a sidecar session on
	// demand and runs the full audited executor loop (tools + git commits
	// + sandbox). Advertised as ToolCalls: true in initialize.
	EnableTools bool

	conn   *Conn
	mu     sync.Mutex
	nextID uint64

	// sessions tracked by ID; one active ACP session can host many agent
	// prompts. For v1 we keep state minimal: just the agent.Message history.
	sessions map[string]*acpSession
}

type acpSession struct {
	id       string
	mu       sync.Mutex
	messages []agent.Message
	cancel   context.CancelFunc
	workdir  string
	gitSess  *stadogit.Session
	busy     bool
}

// NewServer returns a configured ACP server. Provider can be nil; lazy-built
// on first prompt (mirrors the TUI's behaviour so missing API keys don't
// break the handshake).
func NewServer(cfg *config.Config, prov agent.Provider) *Server {
	return &Server{Cfg: cfg, Provider: prov, sessions: map[string]*acpSession{}}
}

// Serve runs the server loop on r/w until the peer disconnects. Blocking.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.conn = NewConn(r, w)
	return s.conn.Serve(ctx, s.dispatch)
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.handleInitialize(params)
	case "session/new":
		return s.handleSessionNew(params)
	case "session/prompt":
		return s.handleSessionPrompt(ctx, params)
	case "session/cancel":
		return s.handleSessionCancel(params)
	case "shutdown":
		s.conn.WaitPendingExceptCaller()
		go s.conn.Close()
		return struct{}{}, nil
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "unknown method: " + method}
}

// --- handlers ---

type initializeResult struct {
	ProtocolVersion int            `json:"protocolVersion"`
	AgentName       string         `json:"agentName"`
	AgentVersion    string         `json:"agentVersion"`
	Capabilities    initializeCaps `json:"capabilities"`
}

type initializeCaps struct {
	Prompts   bool `json:"prompts"`
	ToolCalls bool `json:"toolCalls"`
	Thinking  bool `json:"thinking"`
}

func (s *Server) handleInitialize(_ json.RawMessage) (any, error) {
	return initializeResult{
		ProtocolVersion: ProtocolVersion,
		AgentName:       "stado",
		AgentVersion:    "0.0.0-dev",
		Capabilities: initializeCaps{
			Prompts:   true,
			ToolCalls: s.EnableTools,
			Thinking:  true,
		},
	}, nil
}

type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

func (s *Server) handleSessionNew(_ json.RawMessage) (any, error) {
	cwd, _ := os.Getwd()
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("acp-%d", s.nextID)
	s.sessions[id] = &acpSession{id: id, workdir: cwd}
	s.mu.Unlock()
	return sessionNewResult{SessionID: id}, nil
}

type sessionPromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
}

type sessionPromptResult struct {
	Text string `json:"text"`
}

func (s *Server) handleSessionPrompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sessionPromptParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "unknown sessionId"}
	}

	// Lazy provider init.
	prov := s.Provider
	if prov == nil {
		return nil, &RPCError{Code: CodeInternalError, Message: "no provider configured"}
	}

	sess.mu.Lock()
	if sess.busy {
		sess.mu.Unlock()
		return nil, &RPCError{Code: CodeInvalidParams, Message: "session already has an active prompt"}
	}
	sess.busy = true
	sess.messages = append(sess.messages, agent.Text(agent.RoleUser, p.Prompt))
	localMsgs := append([]agent.Message(nil), sess.messages...)
	workdir := sess.workdir

	pctx, cancel := context.WithCancel(ctx)
	sess.cancel = cancel
	defer func() {
		sess.mu.Lock()
		sess.cancel = nil
		sess.busy = false
		sess.mu.Unlock()
	}()
	sess.mu.Unlock()

	// AGENTS.md / CLAUDE.md injection — same policy as `stado run`.
	// Resolved from the ACP server's process cwd because ACP sessions
	// don't carry a per-session workdir today; fine for the common
	// case where a client spawns stado in-repo.
	sysPrompt := ""
	if workdir != "" {
		if res, err := instructions.Load(workdir); err == nil {
			sysPrompt = res.Content
		}
	}

	opts := runtime.AgentLoopOptions{
		Provider:             prov,
		Model:                s.Cfg.Defaults.Model,
		Messages:             localMsgs,
		MaxTurns:             10, // with tools enabled we may need multiple turns
		Thinking:             s.Cfg.Agent.Thinking,
		ThinkingBudgetTokens: s.Cfg.Agent.ThinkingBudgetTokens,
		System:               sysPrompt,
		OnEvent: func(ev agent.Event) {
			switch ev.Kind {
			case agent.EvTextDelta:
				if ev.Text != "" {
					_ = s.conn.Notify("session/update", map[string]any{
						"sessionId": p.SessionID, "kind": "text", "text": ev.Text,
					})
				}
			case agent.EvToolCallEnd:
				if ev.ToolCall != nil {
					_ = s.conn.Notify("session/update", map[string]any{
						"sessionId": p.SessionID, "kind": "tool_call",
						"name": ev.ToolCall.Name, "input": string(ev.ToolCall.Input),
					})
				}
			}
		},
	}
	if s.EnableTools {
		s.ensureGitSession(sess)
		sess.mu.Lock()
		gitSess := sess.gitSess
		sess.mu.Unlock()
		if gitSess != nil {
			exec, err := runtime.BuildExecutor(gitSess, s.Cfg, "stado-acp")
			if err != nil {
				return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
			}
			opts.Executor = exec
		}
	} else {
		opts.MaxTurns = 1 // pure chat: single shot
	}

	text, msgs, err := runtime.AgentLoop(pctx, opts)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	sess.mu.Lock()
	sess.messages = msgs
	sess.mu.Unlock()
	return sessionPromptResult{Text: text}, nil
}

type sessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

func (s *Server) handleSessionCancel(raw json.RawMessage) (any, error) {
	var p sessionCancelParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "unknown sessionId"}
	}
	sess.mu.Lock()
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.mu.Unlock()
	return struct{}{}, nil
}

func (s *Server) ensureGitSession(sess *acpSession) {
	sess.mu.Lock()
	if sess.gitSess != nil || sess.workdir == "" {
		sess.mu.Unlock()
		return
	}
	workdir := sess.workdir
	sess.mu.Unlock()

	gs, err := runtime.OpenSession(s.Cfg, workdir)
	if err != nil {
		return
	}

	sess.mu.Lock()
	if sess.gitSess == nil {
		sess.gitSess = gs
	}
	sess.mu.Unlock()
}
