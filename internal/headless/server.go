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
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

// Server is the headless JSON-RPC daemon.
type Server struct {
	Cfg      *config.Config
	Provider agent.Provider

	conn     *acp.Conn
	mu       sync.Mutex
	sessions map[string]*hSession
}

type hSession struct {
	id       string
	messages []agent.Message
	executor interface{} // runtime.Executor when tools enabled; nil otherwise
	cancel   context.CancelFunc
	workdir  string
}

func NewServer(cfg *config.Config, prov agent.Provider) *Server {
	return &Server{Cfg: cfg, Provider: prov, sessions: map[string]*hSession{}}
}

// Serve runs the loop on r/w until the peer disconnects.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.conn = acp.NewConn(r, w)
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
	case "tools.list":
		return s.toolsList(), nil
	case "providers.list":
		return map[string]any{
			"available": []string{"anthropic", "openai", "google", "ollama", "llamacpp", "vllm"},
			"current":   s.Cfg.Defaults.Provider,
		}, nil
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
		Provider: s.Provider,
		Model:    s.Cfg.Defaults.Model,
		Messages: sess.messages,
		MaxTurns: 10,
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
		},
	}
	if p.Tools {
		gitSess, err := runtime.OpenSession(s.Cfg, sess.workdir)
		if err == nil {
			opts.Executor = runtime.BuildExecutor(gitSess, s.Cfg, "stado-headless")
		}
	}

	text, msgs, err := runtime.AgentLoop(pctx, opts)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	sess.messages = msgs
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
