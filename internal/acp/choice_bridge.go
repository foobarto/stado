package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// pendingChoiceRegistry tracks in-flight stado_ui_choose requests
// emitted on session/update kind=choice. Keyed by request id; the
// channel is closed-over by RequestChoice and signalled when the
// matching session/choice_response RPC arrives. Single-flight per
// session is enforced separately on the wasm side (only one request
// from one plugin call is alive at a time); this map exists to
// correlate incoming responses across concurrent sessions.
type pendingChoiceRegistry struct {
	mu      sync.Mutex
	next    uint64
	pending map[string]*pendingChoice
}

type pendingChoice struct {
	sessionID string
	respCh    chan pluginRuntime.ChoiceResponse
}

func newPendingChoiceRegistry() *pendingChoiceRegistry {
	return &pendingChoiceRegistry{pending: map[string]*pendingChoice{}}
}

// allocate returns a fresh request id and the channel to receive on.
func (r *pendingChoiceRegistry) allocate(sessionID string) (string, chan pluginRuntime.ChoiceResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := fmt.Sprintf("choice-%d", r.next)
	ch := make(chan pluginRuntime.ChoiceResponse, 1)
	r.pending[id] = &pendingChoice{sessionID: sessionID, respCh: ch}
	return id, ch
}

// resolve delivers the response and clears the entry. Returns false
// when the id is unknown (late or duplicate response from the
// client).
func (r *pendingChoiceRegistry) resolve(id string, resp pluginRuntime.ChoiceResponse) bool {
	r.mu.Lock()
	pc, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pc.respCh <- resp:
	default:
	}
	return true
}

// remove clears an entry without resolving (caller already returned;
// late responses just no-op). Used on ctx cancel paths.
func (r *pendingChoiceRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

// cancelSession resolves every pending request for the given session
// with cancelled=true. Called when the operator cancels the session
// or the connection drops, so plugin calls don't deadlock.
func (r *pendingChoiceRegistry) cancelSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, pc := range r.pending {
		if pc.sessionID != sessionID {
			continue
		}
		select {
		case pc.respCh <- pluginRuntime.ChoiceResponse{Cancelled: true}:
		default:
		}
		delete(r.pending, id)
	}
}

// requestChoice is the ACP-side ChoiceBridge entry point. Allocates a
// request id, emits session/update kind=choice, and blocks on the
// matching session/choice_response. ctx cancellation cancels the
// pending request and returns ChoiceResponse{Cancelled:true} promptly.
func (s *Server) requestChoice(ctx context.Context, sessionID string, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	if s == nil || s.conn == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("acp server not initialised")
	}
	if s.choiceRegistry == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("choice registry unavailable")
	}
	id, ch := s.choiceRegistry.allocate(sessionID)

	options := make([]map[string]string, 0, len(req.Options))
	for _, o := range req.Options {
		options = append(options, map[string]string{"id": o.ID, "label": o.Label})
	}
	if err := s.conn.Notify("session/update", map[string]any{
		"sessionId": sessionID,
		"kind":      "choice",
		"requestId": id,
		"prompt":    req.Prompt,
		"options":   options,
		"multi":     req.Multi,
		"default":   req.Default,
	}); err != nil {
		s.choiceRegistry.remove(id)
		return pluginRuntime.ChoiceResponse{}, fmt.Errorf("acp notify: %w", err)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		s.choiceRegistry.remove(id)
		return pluginRuntime.ChoiceResponse{Cancelled: true}, ctx.Err()
	}
}

// sessionChoiceResponseParams is the wire shape the ACP client sends
// to deliver an operator's choice. Symmetric with the kind=choice
// notification.
type sessionChoiceResponseParams struct {
	SessionID string   `json:"sessionId"`
	RequestID string   `json:"requestId"`
	Selected  []string `json:"selected"`
	Cancelled bool     `json:"cancelled"`
}

// handleSessionChoiceResponse routes an incoming
// session/choice_response RPC into the pending registry. Returns an
// empty ack on successful delivery, an error when the request id is
// unknown (typically a late response after ctx cancellation).
func (s *Server) handleSessionChoiceResponse(raw json.RawMessage) (any, error) {
	var p sessionChoiceResponseParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if p.RequestID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "requestId required"}
	}
	if s.choiceRegistry == nil {
		return nil, &RPCError{Code: CodeInternalError, Message: "choice registry unavailable"}
	}
	resp := pluginRuntime.ChoiceResponse{Selected: p.Selected, Cancelled: p.Cancelled}
	if !s.choiceRegistry.resolve(p.RequestID, resp) {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "unknown requestId"}
	}
	return struct{}{}, nil
}
