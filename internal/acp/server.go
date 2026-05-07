package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// ProtocolVersion advertised in the initialize handshake. Bumping requires
// coordinated update in stado + any ACP client.
//
// session/update notifications carry one of these kinds today:
//   - "text"      — streaming text delta from the provider; payload field "text".
//   - "tool_call" — a tool call has completed (success or error); payload fields
//     "name" (string) and "input" (string — JSON-encoded args object).
//   - "subagent"  — subagent lifecycle event; payload includes phase, status,
//     role, mode, child session id, parent session, etc.
//   - "choice"    — a wasm plugin called stado_ui_choose; payload fields
//     "requestId" (string), "prompt" (string), "options" ([{id,label}]),
//     "multi" (bool), "default" ([]string). The client MUST reply with
//     session/choice_response{sessionId, requestId, selected:[]string,
//     cancelled:bool} or the plugin call blocks until session/cancel.
//   - "approval"  — a wasm plugin called stado_ui_approve; payload fields
//     "requestId" (string), "title" (string), "body" (string). The client
//     MUST reply with session/approval_response{sessionId, requestId,
//     allow:bool, cancelled:bool} or the plugin call blocks until
//     session/cancel.
//
// session/new accepts an optional `maxTurns` integer to pin the per-session turn
// budget; falls back to [acp] max_turns from config, then 50 with --tools / 1
// without. ACP clients that need long-running tool sessions should set this
// explicitly rather than relying on the defaults.
//
// session/new also accepts an optional `resumeSession` string carrying a
// canonical session UUID to resume. When set, the server opens that
// session's existing worktree, loads the prior conversation history,
// and returns the same UUID as the ACP `sessionId` so the wire id
// matches the git id. Falls back to the operator-set
// `stado acp --resume <id-or-label>` default when the caller leaves
// `resumeSession` empty. Resuming a session that's already active in
// this server returns CodeInvalidParams to keep history coherent.
const ProtocolVersion = 1

// Server is the stado ACP server — stdin/stdout JSON-RPC, one connection.
type Server struct {
	Cfg      *config.Config
	Provider agent.Provider

	// EnableTools, if set, means session.prompt opens a sidecar session on
	// demand and runs the full audited executor loop (tools + git commits
	// + sandbox). Advertised as ToolCalls: true in initialize.
	EnableTools bool

	// ResumeSessionID is the canonical git-native session id the
	// operator pinned via `stado acp --resume <id>`. Applied as the
	// default for `session/new` when the caller's `resumeSession`
	// param is empty. Resolved (prefix / description lookup) before
	// the server starts; this field is always a full session UUID.
	ResumeSessionID string

	// DefaultPersona is the operator's persona pin from
	// `stado acp --persona <name>`. Applied to every `session/new`
	// when the caller's `persona` param is empty. Resolved by the CLI
	// layer before the server starts so the wire never sees a
	// missing-persona error after the handshake.
	DefaultPersona *personas.Persona

	conn   *Conn
	mu     sync.Mutex
	nextID uint64

	// sessions tracked by ID; one active ACP session can host many agent
	// prompts. For v1 we keep state minimal: just the agent.Message history.
	sessions map[string]*acpSession

	// choiceRegistry tracks in-flight stado_ui_choose requests emitted as
	// session/update kind=choice. Resolved by session/choice_response
	// RPCs from the client. Q3 Phase B.
	choiceRegistry *pendingChoiceRegistry

	// approvalRegistry tracks in-flight stado_ui_approve requests emitted
	// as session/update kind=approval. Resolved by session/approval_response
	// RPCs from the client. Symmetric with choiceRegistry.
	approvalRegistry *pendingApprovalRegistry
}

type acpSession struct {
	id               string
	mu               sync.Mutex
	messages         []agent.Message
	cancel           context.CancelFunc
	workdir          string
	gitSess          *stadogit.Session
	persistedViewLen int
	busy             bool

	// maxTurns is the per-session cap chosen by the caller via
	// `session/new`. Zero means "use the server-level default"
	// (Server.Cfg.ACP.MaxTurns, then the built-in fallback).
	maxTurns int

	// persona is the resolved persona for this session. Set at
	// session/new from `persona` param (per-call) or
	// Server.DefaultPersona (operator CLI pin). nil keeps the legacy
	// ComposeSystemPrompt path inside AgentLoop, which is what the
	// pre-personas behaviour was.
	persona *personas.Persona
}

// NewServer returns a configured ACP server. Provider can be nil; lazy-built
// on first prompt (mirrors the TUI's behaviour so missing API keys don't
// break the handshake).
func NewServer(cfg *config.Config, prov agent.Provider) *Server {
	return &Server{
		Cfg:              cfg,
		Provider:         prov,
		sessions:         map[string]*acpSession{},
		choiceRegistry:   newPendingChoiceRegistry(),
		approvalRegistry: newPendingApprovalRegistry(),
	}
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
	case "session/choice_response":
		return s.handleSessionChoiceResponse(params)
	case "session/approval_response":
		return s.handleSessionApprovalResponse(params)
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

// resolveMaxTurns picks the per-prompt turn budget for an ACP session,
// in this priority order:
//  1. session/new's `maxTurns` param (per-session pin from the caller)
//  2. [acp] max_turns from config.toml (operator default)
//  3. 50 when --tools, 1 otherwise (built-in fallback)
//
// Per-session pins below 1 are coerced to 1 so the loop always
// runs at least one turn. The non-tools "1 turn" default is applied
// at the call site when both #1 and #2 are unset.
func (s *Server) resolveMaxTurns(sess *acpSession) int {
	if sess != nil && sess.maxTurns > 0 {
		return sess.maxTurns
	}
	if s.Cfg != nil && s.Cfg.ACP.MaxTurns > 0 {
		return s.Cfg.ACP.MaxTurns
	}
	if s.EnableTools {
		return 50
	}
	return 1
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

type sessionNewParams struct {
	// MaxTurns lets the ACP client pin a per-session turn budget. Zero
	// or omitted falls back to the server config ([acp] max_turns),
	// then to the built-in default. Caps below 1 are coerced to 1
	// to keep the loop progressing on at least one turn. v0.45.1.
	MaxTurns int `json:"maxTurns"`

	// ResumeSession, when non-empty, asks the server to attach to an
	// existing git-native session by full UUID instead of starting a
	// fresh ACP session. Empty falls back to Server.ResumeSessionID
	// (the operator's `--resume` CLI default). Returned `sessionId`
	// matches this id verbatim so the ACP client can round-trip it.
	// Unknown ids surface as a CodeInvalidParams error before the
	// session is registered.
	ResumeSession string `json:"resumeSession"`

	// Persona, when non-empty, names a persona to apply to this
	// session's turns. Resolution order: project (`{cwd}/.stado/personas/`)
	// → user (`<config-dir>/personas/`) → bundled. Empty falls back
	// to Server.DefaultPersona (the operator's `--persona` CLI pin),
	// then to the AgentLoop legacy path. Unknown names surface as a
	// CodeInvalidParams error before the session is registered.
	Persona string `json:"persona"`
}

type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

func (s *Server) handleSessionNew(raw json.RawMessage) (any, error) {
	var p sessionNewParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if s.EnableTools {
		if msg := s.checkInstalledPluginABI(); msg != "" {
			return nil, &RPCError{Code: CodeInternalError, Message: msg}
		}
	}

	// Persona resolution: per-call name beats operator default. Empty
	// per-call + nil default = leave persona unset, AgentLoop falls
	// back to its legacy ComposeSystemPrompt path.
	persona, perr := s.resolveSessionPersona(p.Persona)
	if perr != nil {
		return nil, perr
	}

	// Resume target precedence: per-call param > operator-set CLI
	// default. Both forms must be a full session UUID; prefix /
	// description lookup stays in the CLI layer (cmd/stado).
	resumeID := p.ResumeSession
	if resumeID == "" {
		resumeID = s.ResumeSessionID
	}
	if resumeID != "" {
		sess, err := s.buildResumedSession(resumeID, p.MaxTurns)
		if err != nil {
			return nil, err
		}
		sess.persona = persona
		s.mu.Lock()
		if _, taken := s.sessions[resumeID]; taken {
			s.mu.Unlock()
			return nil, &RPCError{
				Code:    CodeInvalidParams,
				Message: "session already active in this server: " + resumeID,
			}
		}
		s.sessions[resumeID] = sess
		s.mu.Unlock()
		return sessionNewResult{SessionID: resumeID}, nil
	}

	cwd, _ := os.Getwd()
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("acp-%d", s.nextID)
	s.sessions[id] = &acpSession{
		id:       id,
		workdir:  cwd,
		maxTurns: p.MaxTurns,
		persona:  persona,
	}
	s.mu.Unlock()
	return sessionNewResult{SessionID: id}, nil
}

// resolveSessionPersona picks the persona for a new session.
// Per-call name (from session/new) wins; a non-empty name that
// doesn't resolve is a hard error so the caller learns about a
// typo before the first turn fires. Empty per-call name uses
// Server.DefaultPersona — already resolved by the CLI layer.
func (s *Server) resolveSessionPersona(name string) (*personas.Persona, *RPCError) {
	if name == "" {
		return s.DefaultPersona, nil
	}
	cwd, _ := os.Getwd()
	cfgDir := ""
	if s.Cfg != nil {
		cfgDir = config.ConfigDir()
	}
	p, err := (personas.Resolver{CWD: cwd, ConfigDir: cfgDir}).Load(name)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInvalidParams,
			Message: "persona: " + err.Error(),
		}
	}
	return p, nil
}

// buildResumedSession opens an existing git-native session by id,
// loads its conversation history into a fresh acpSession, and
// returns it ready to be registered. Validates the id format and
// returns RPC-shaped errors so handleSessionNew can pass them
// straight back to the caller.
func (s *Server) buildResumedSession(id string, maxTurns int) (*acpSession, error) {
	if err := stadogit.ValidateSessionID(id); err != nil {
		return nil, &RPCError{
			Code:    CodeInvalidParams,
			Message: "resumeSession: invalid session id: " + err.Error(),
		}
	}
	// Tighter than ValidateSessionID (which only rejects path
	// traversals): the wire contract says canonical UUID. Catch
	// callers passing labels or prefixes here so the error points
	// at the bug instead of at "no worktree".
	if _, err := uuid.Parse(id); err != nil {
		return nil, &RPCError{
			Code:    CodeInvalidParams,
			Message: "resumeSession: invalid session id (must be a canonical UUID; prefix / description lookup is operator-only via --resume): " + err.Error(),
		}
	}
	if s.Cfg == nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: "resumeSession: server has no config",
		}
	}
	worktreePath := filepath.Join(s.Cfg.WorktreeDir(), id)
	if _, err := os.Stat(worktreePath); err != nil {
		if os.IsNotExist(err) {
			return nil, &RPCError{
				Code:    CodeInvalidParams,
				Message: "resumeSession: no session worktree for id " + id,
			}
		}
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: "resumeSession: stat worktree: " + err.Error(),
		}
	}
	gitSess, err := runtime.OpenSessionByID(s.Cfg, worktreePath, id)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInvalidParams,
			Message: "resumeSession: open session " + id + ": " + err.Error(),
		}
	}
	priorMsgs, err := runtime.LoadConversation(worktreePath)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: "resumeSession: load conversation: " + err.Error(),
		}
	}
	return &acpSession{
		id:               id,
		workdir:          worktreePath,
		maxTurns:         maxTurns,
		gitSess:          gitSess,
		messages:         priorMsgs,
		persistedViewLen: len(priorMsgs),
	}, nil
}

// checkInstalledPluginABI eagerly verifies installed-plugin wasm
// modules export the required ABI surface (stado_alloc, stado_free,
// stado_tool_<name>). Returns an empty string when everything checks
// out, or a multi-line summary suitable for an RPC error message.
//
// Surfaced from session/new so ACP integrators see the broken-plugin
// diagnostic ONCE, before any prompt — instead of the model spinning
// through retries against a stale plugin and failing each tool call
// with no actionable cue. v0.45.1, fix for B2.
func (s *Server) checkInstalledPluginABI() string {
	if s.Cfg == nil {
		return ""
	}
	issues, err := runtime.VerifyInstalledPluginsABI(context.Background(), s.Cfg)
	if err != nil {
		// Treat enumeration failure as a soft warning — the per-tool
		// invocation path will surface the real error if the plugin
		// is unusable. Don't block session/new on this.
		fmt.Fprintf(os.Stderr, "stado acp: warn: ABI verify enumeration failed: %v\n", err)
		return ""
	}
	if len(issues) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "installed plugin ABI mismatch — rebuild required for:")
	for _, i := range issues {
		lines = append(lines, "  "+i.String())
	}
	lines = append(lines, "rebuild plugin(s) against the current stado runtime, re-sign, and re-install before starting a new session.")
	return strings.Join(lines, "\n")
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
		MaxTurns:             s.resolveMaxTurns(sess),
		Persona:              sess.persona,
		Thinking:             s.Cfg.Agent.Thinking,
		ThinkingBudgetTokens: s.Cfg.Agent.ThinkingBudgetTokens,
		System:               sysPrompt,
		SystemTemplate:       s.Cfg.Agent.SystemPromptTemplate,
		MemoryContext:        s.memoryPromptContext(pctx, workdir, p.SessionID, p.Prompt),
		OnSubagentEvent: func(ev runtime.SubagentEvent) {
			s.emitSubagentUpdate(p.SessionID, ev)
		},
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
			opts.Host = &acpHost{
				server:    s,
				sessionID: p.SessionID,
				workdir:   workdir,
				readLog:   exec.ReadLog,
				runner:    exec.Runner,
			}
		}
	} else if sess.maxTurns == 0 && (s.Cfg == nil || s.Cfg.ACP.MaxTurns == 0) {
		opts.MaxTurns = 1 // pure chat default: single shot when nobody asked for more
	}

	text, msgs, err := runtime.AgentLoop(pctx, opts)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	sess.mu.Lock()
	gitSess := sess.gitSess
	persistedViewLen := sess.persistedViewLen
	sess.mu.Unlock()
	if gitSess != nil {
		nextPersisted, err := runtime.AppendMessagesFrom(gitSess.WorktreePath, msgs, persistedViewLen)
		sess.mu.Lock()
		sess.persistedViewLen = nextPersisted
		sess.mu.Unlock()
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
	}
	sess.mu.Lock()
	sess.messages = msgs
	sess.mu.Unlock()
	return sessionPromptResult{Text: text}, nil
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
	_ = s.conn.Notify("session/update", payload)
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
	// Resolve any pending choice / approval requests for this session
	// so plugin calls don't deadlock waiting for a client that's about
	// to drop the prompt. Q3 Phase B + approval-bridge follow-up.
	if s.choiceRegistry != nil {
		s.choiceRegistry.cancelSession(p.SessionID)
	}
	if s.approvalRegistry != nil {
		s.approvalRegistry.cancelSession(p.SessionID)
	}
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
