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
	"sort"
	"sync"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
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
	nextID   uint64 // monotonic counter so deleting sessions doesn't reuse IDs

	// Background-plugin state. See plugins.go. Populated on Serve()
	// entry from cfg.Plugins.Background and torn down on exit.
	bgRuntime *pluginRuntime.Runtime
	bgPlugins []*pluginRuntime.BackgroundPlugin
}

type hSession struct {
	id              string
	mu              sync.Mutex
	messages        []agent.Message
	cancel          context.CancelFunc
	workdir         string
	gitSess         *stadogit.Session // lazy, set by ensureGitSession
	lastInputTokens int               // most recent input-token observation
	busy            bool
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
		return s.toolsList()
	case "providers.list":
		// `current` reflects what the server actually resolved, not
		// what's written in config — the local-fallback path leaves
		// cfg.Defaults.Provider empty even when a runner is serving.
		// Clients call this to learn which provider they're talking
		// to; blank when neither config nor a resolved runner applies.
		current := s.Cfg.Defaults.Provider
		if s.Provider != nil {
			current = s.Provider.Name()
		}
		return map[string]any{
			"available": availableProviders(s.Cfg),
			"current":   current,
		}, nil
	case "plugin.list":
		return s.pluginList(), nil
	case "plugin.run":
		return s.pluginRun(ctx, params)
	case "shutdown":
		// Wait for every other in-flight dispatch to complete before
		// we reply — otherwise shutdown races ahead of slow calls like
		// plugin.run, and the client sees responses arriving *after*
		// the shutdown ACK. Conn.Close then runs on the background
		// drain path in a fresh goroutine so this dispatch can return
		// + its response can flush before we tear down the pipe.
		s.conn.WaitPendingExceptCaller()
		go s.conn.Close()
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
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("h-%d", s.nextID)
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
			// headless emits session.update{kind:"context_warning"} when
			// soft threshold is crossed. Fired on Usage events (end of
			// turn) so clients see it before the next prompt.
			if (ev.Kind == agent.EvUsage || ev.Kind == agent.EvDone) && ev.Usage != nil {
				sess.mu.Lock()
				sess.lastInputTokens = ev.Usage.InputTokens
				sess.mu.Unlock()
				s.maybeEmitContextWarning(p.SessionID, ev.Usage.InputTokens)
			}
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
		sess.mu.Lock()
		msgs := sess.messages
		sess.mu.Unlock()
		out = append(out, sessionListItem{
			SessionID: sess.id,
			Turns:     countAssistantTurns(msgs),
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

func (s *Server) toolsList() (any, error) {
	exec, err := runtime.BuildExecutor(nil, s.Cfg, "stado-headless")
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	all := exec.Registry.All()
	out := make([]toolInfo, 0, len(all))
	for _, t := range all {
		cls := exec.Registry.ClassOf(t.Name()).String()
		out = append(out, toolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			Class:       cls,
		})
	}
	return out, nil
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
	sess.mu.Lock()
	cancelled := sess.cancel != nil
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.mu.Unlock()
	return struct {
		Cancelled bool `json:"cancelled"`
	}{Cancelled: cancelled}, nil
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
	Summary    string `json:"summary"`
	PriorTurns int    `json:"priorTurns"`
	PostTurns  int    `json:"postTurns"`
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
	sess.mu.Lock()
	if sess.busy {
		sess.mu.Unlock()
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "session already has an active operation"}
	}
	if len(sess.messages) == 0 {
		sess.mu.Unlock()
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "session has no messages to compact"}
	}
	sess.busy = true
	// Copy snapshot and release lock before the slow provider call so
	// concurrent session.cancel / session.prompt aren't blocked.
	msgs := append(make([]agent.Message, 0, len(sess.messages)), sess.messages...)
	defer func() {
		sess.mu.Lock()
		sess.busy = false
		sess.mu.Unlock()
	}()
	sess.mu.Unlock()

	summary, err := compact.Summarise(ctx, s.Provider, s.Cfg.Defaults.Model, msgs)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}

	sess.mu.Lock()
	prior := len(sess.messages)
	sess.messages = compact.ReplaceMessages(summary)
	post := len(sess.messages)
	sess.mu.Unlock()
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

func availableProviders(cfg *config.Config) []string {
	set := map[string]struct{}{
		"anthropic":  {},
		"openai":     {},
		"google":     {},
		"gemini":     {},
		"ollama":     {},
		"llamacpp":   {},
		"vllm":       {},
		"lmstudio":   {},
		"groq":       {},
		"openrouter": {},
		"deepseek":   {},
		"xai":        {},
		"mistral":    {},
		"cerebras":   {},
		"litellm":    {},
	}
	if cfg != nil {
		for name, preset := range cfg.Inference.Presets {
			if preset.Endpoint != "" {
				set[name] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
