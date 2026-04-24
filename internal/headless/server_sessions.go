package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

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
// or confirm step — the replacement happens before the result is
// returned. Call session.prompt next to continue from the compacted
// state.
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
		rawLogSHA, err := runtime.ConversationLogSHA(gs.WorktreePath)
		if err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
		}
		fromTurn, toTurn, turnsTotal := headlessCompactionTurnRange(gs, prior)
		treeSHA, traceSHA, err := gs.CommitCompaction(stadogit.CompactionMeta{
			Title:      "headless session.compact",
			Summary:    summary,
			FromTurn:   fromTurn,
			ToTurn:     toTurn,
			TurnsTotal: turnsTotal,
			ByAuthor:   "stado-headless",
			RawLogSHA:  rawLogSHA,
		})
		if err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
		}
		if err := runtime.AppendCompaction(gs.WorktreePath, runtime.ConversationCompaction{
			Summary:    summary,
			FromTurn:   fromTurn,
			ToTurn:     toTurn,
			TurnsTotal: turnsTotal,
			By:         "stado-headless",
			TreeSHA:    treeSHA.String(),
			TraceSHA:   traceSHA.String(),
			RawLogSHA:  rawLogSHA,
		}); err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
		}
	}
	sess.mu.Lock()
	sess.messages = compact.ReplaceMessages(summary)
	if gs != nil {
		sess.persistedViewLen = len(sess.messages)
	}
	post := len(sess.messages)
	sess.mu.Unlock()
	return sessionCompactResult{Summary: summary, PriorTurns: prior, PostTurns: post}, nil
}

func headlessCompactionTurnRange(gs *stadogit.Session, fallbackMessages int) (fromTurn, toTurn, turnsTotal int) {
	if gs == nil {
		return 0, 0, fallbackMessages
	}
	toTurn = gs.Turn()
	if markers, err := gs.Sidecar.ListCompactions(gs.ID); err == nil && len(markers) > 0 {
		fromTurn = markers[0].ToTurn + 1
	}
	switch {
	case toTurn <= 0:
		turnsTotal = fallbackMessages
	case fromTurn == 0:
		turnsTotal = toTurn
	case toTurn >= fromTurn:
		turnsTotal = toTurn - fromTurn + 1
	default:
		turnsTotal = fallbackMessages
	}
	return fromTurn, toTurn, turnsTotal
}
