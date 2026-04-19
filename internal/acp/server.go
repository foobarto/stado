package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

// ProtocolVersion advertised in the initialize handshake. Bumping requires
// coordinated update in stado + any ACP client.
const ProtocolVersion = 1

// Server is the stado ACP server — stdin/stdout JSON-RPC, one connection.
type Server struct {
	Cfg      *config.Config
	Provider agent.Provider

	conn *Conn
	mu   sync.Mutex

	// sessions tracked by ID; one active ACP session can host many agent
	// prompts. For v1 we keep state minimal: just the agent.Message history.
	sessions map[string]*acpSession
}

type acpSession struct {
	id       string
	messages []agent.Message
	cancel   context.CancelFunc
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
		s.conn.Close()
		return struct{}{}, nil
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "unknown method: " + method}
}

// --- handlers ---

type initializeParams struct {
	ClientName    string `json:"clientName,omitempty"`
	ClientVersion string `json:"clientVersion,omitempty"`
}

type initializeResult struct {
	ProtocolVersion int                 `json:"protocolVersion"`
	AgentName       string              `json:"agentName"`
	AgentVersion    string              `json:"agentVersion"`
	Capabilities    initializeCaps      `json:"capabilities"`
}

type initializeCaps struct {
	Prompts    bool `json:"prompts"`
	ToolCalls  bool `json:"toolCalls"`
	Thinking   bool `json:"thinking"`
}

func (s *Server) handleInitialize(_ json.RawMessage) (any, error) {
	return initializeResult{
		ProtocolVersion: ProtocolVersion,
		AgentName:       "stado",
		AgentVersion:    "0.0.0-dev",
		Capabilities: initializeCaps{
			Prompts:   true,
			ToolCalls: false, // v1: tool calls aren't plumbed through ACP yet
			Thinking:  true,
		},
	}, nil
}

type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

func (s *Server) handleSessionNew(_ json.RawMessage) (any, error) {
	id := fmt.Sprintf("acp-%d", len(s.sessions)+1)
	s.mu.Lock()
	s.sessions[id] = &acpSession{id: id}
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

	pctx, cancel := context.WithCancel(ctx)
	sess.cancel = cancel
	defer func() { sess.cancel = nil }()

	sess.messages = append(sess.messages, agent.Text(agent.RoleUser, p.Prompt))

	// Stream; as events arrive, notify the client with session/update.
	text, _, err := runtime.AgentLoop(pctx, runtime.AgentLoopOptions{
		Provider: prov,
		Model:    s.Cfg.Defaults.Model,
		Messages: sess.messages,
		MaxTurns: 1, // ACP v1 = one turn per prompt; multi-turn via subsequent prompts
		OnEvent: func(ev agent.Event) {
			if ev.Kind == agent.EvTextDelta && ev.Text != "" {
				_ = s.conn.Notify("session/update", map[string]any{
					"sessionId": p.SessionID,
					"kind":      "text",
					"text":      ev.Text,
				})
			}
		},
	})
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	sess.messages = append(sess.messages, agent.Message{Role: agent.RoleAssistant, Content: []agent.Block{
		{Text: &agent.TextBlock{Text: text}},
	}})
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
	if sess.cancel != nil {
		sess.cancel()
	}
	return struct{}{}, nil
}
