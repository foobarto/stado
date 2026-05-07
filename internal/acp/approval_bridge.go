package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// pendingApprovalRegistry tracks in-flight stado_ui_approve requests
// emitted on session/update kind=approval. Symmetric with
// pendingChoiceRegistry — ACP needed two registries because
// approval and choice carry different response payloads (bool vs.
// selected[]+cancelled), and conflating them would cost more in
// type churn than the duplication costs in lines.
type pendingApprovalRegistry struct {
	mu      sync.Mutex
	next    uint64
	pending map[string]*pendingApproval
}

type pendingApproval struct {
	sessionID string
	respCh    chan approvalOutcome
}

// approvalOutcome carries the operator's verdict back to the bridge
// goroutine. allow=false with cancelled=true means the operator
// dismissed the prompt rather than denying it; the bridge collapses
// both to allow=false at the wasm boundary (stado_ui_approve only has
// allow/deny/unavailable), but the distinction is kept in the wire
// format so future clients can surface it.
type approvalOutcome struct {
	allow     bool
	cancelled bool
}

func newPendingApprovalRegistry() *pendingApprovalRegistry {
	return &pendingApprovalRegistry{pending: map[string]*pendingApproval{}}
}

// allocate returns a fresh request id and the channel to receive on.
func (r *pendingApprovalRegistry) allocate(sessionID string) (string, chan approvalOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := fmt.Sprintf("approval-%d", r.next)
	ch := make(chan approvalOutcome, 1)
	r.pending[id] = &pendingApproval{sessionID: sessionID, respCh: ch}
	return id, ch
}

// resolve delivers the response and clears the entry. Returns false
// when the id is unknown (typically a late or duplicate response from
// the client).
func (r *pendingApprovalRegistry) resolve(id string, out approvalOutcome) bool {
	r.mu.Lock()
	pa, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pa.respCh <- out:
	default:
	}
	return true
}

// remove clears an entry without resolving. Used on ctx cancel paths
// so late client responses no-op rather than racing.
func (r *pendingApprovalRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

// cancelSession resolves every pending approval for the given session
// with cancelled=true. Called when the operator cancels the session
// or the connection drops, so plugin calls don't deadlock.
func (r *pendingApprovalRegistry) cancelSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, pa := range r.pending {
		if pa.sessionID != sessionID {
			continue
		}
		select {
		case pa.respCh <- approvalOutcome{cancelled: true}:
		default:
		}
		delete(r.pending, id)
	}
}

// requestApproval is the ACP-side ApprovalBridge entry point.
// Allocates a request id, emits session/update kind=approval, and
// blocks on the matching session/approval_response. ctx cancellation
// removes the pending entry and returns (false, ctx.Err()) promptly.
func (s *Server) requestApproval(ctx context.Context, sessionID, title, body string) (bool, error) {
	if s == nil || s.conn == nil {
		return false, errors.New("acp server not initialised")
	}
	if s.approvalRegistry == nil {
		return false, errors.New("approval registry unavailable")
	}
	id, ch := s.approvalRegistry.allocate(sessionID)
	if err := s.conn.Notify("session/update", map[string]any{
		"sessionId": sessionID,
		"kind":      "approval",
		"requestId": id,
		"title":     title,
		"body":      body,
	}); err != nil {
		s.approvalRegistry.remove(id)
		return false, fmt.Errorf("acp notify: %w", err)
	}
	select {
	case out := <-ch:
		return out.allow, nil
	case <-ctx.Done():
		s.approvalRegistry.remove(id)
		return false, ctx.Err()
	}
}

// sessionApprovalResponseParams is the wire shape the ACP client
// sends to deliver an operator's approval verdict. Symmetric with the
// kind=approval notification.
type sessionApprovalResponseParams struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Allow     bool   `json:"allow"`
	Cancelled bool   `json:"cancelled"`
}

// handleSessionApprovalResponse routes an incoming
// session/approval_response RPC into the pending registry. Returns an
// empty ack on successful delivery, an error when the request id is
// unknown (typically a late response after ctx cancellation).
func (s *Server) handleSessionApprovalResponse(raw json.RawMessage) (any, error) {
	var p sessionApprovalResponseParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if p.RequestID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "requestId required"}
	}
	if s.approvalRegistry == nil {
		return nil, &RPCError{Code: CodeInternalError, Message: "approval registry unavailable"}
	}
	out := approvalOutcome{allow: p.Allow && !p.Cancelled, cancelled: p.Cancelled}
	if !s.approvalRegistry.resolve(p.RequestID, out) {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "unknown requestId"}
	}
	return struct{}{}, nil
}
