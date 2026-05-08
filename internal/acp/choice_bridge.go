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
	// F10 ACP follow-on: hold the original request so the response
	// handler can validate input_value against the chosen option's
	// validator before resolving. Without this, an ACP client could
	// supply input that the TUI bridge would reject — different
	// surfaces would have different validation semantics, which
	// breaks the F10 promise that validators are runtime-side.
	req pluginRuntime.ChoiceRequest
}

func newPendingChoiceRegistry() *pendingChoiceRegistry {
	return &pendingChoiceRegistry{pending: map[string]*pendingChoice{}}
}

// allocate returns a fresh request id and the channel to receive on.
// The request is stored so the response handler can validate
// input_value against the chosen option's validator before resolving
// (F10 ACP follow-on).
func (r *pendingChoiceRegistry) allocate(sessionID string, req pluginRuntime.ChoiceRequest) (string, chan pluginRuntime.ChoiceResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := fmt.Sprintf("choice-%d", r.next)
	ch := make(chan pluginRuntime.ChoiceResponse, 1)
	r.pending[id] = &pendingChoice{sessionID: sessionID, respCh: ch, req: req}
	return id, ch
}

// peek returns the stored request for an id without removing the
// entry. Used by the response handler to validate input_value
// before deciding whether to resolve. Returns ok=false on unknown
// ids. F10 ACP follow-on.
func (r *pendingChoiceRegistry) peek(id string) (pluginRuntime.ChoiceRequest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pc, ok := r.pending[id]
	if !ok {
		return pluginRuntime.ChoiceRequest{}, false
	}
	return pc.req, true
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
	id, ch := s.choiceRegistry.allocate(sessionID, req)

	options := make([]map[string]any, 0, len(req.Options))
	for _, o := range req.Options {
		entry := map[string]any{"id": o.ID, "label": o.Label}
		if o.Prefix != "" {
			entry["prefix"] = o.Prefix
		}
		if o.Input != nil {
			input := map[string]any{"default": o.Input.Default}
			if o.Input.Validator != nil {
				validator := map[string]string{"kind": o.Input.Validator.Kind}
				if o.Input.Validator.Spec != "" {
					validator["spec"] = o.Input.Validator.Spec
				}
				input["validator"] = validator
			}
			entry["input"] = input
		}
		options = append(options, entry)
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
//
// F10 ACP follow-on: InputValue carries the operator-typed text from
// the chosen option's input field. "" when the chosen option had no
// input or when the client doesn't yet implement input rendering
// (graceful degradation).
type sessionChoiceResponseParams struct {
	SessionID  string   `json:"sessionId"`
	RequestID  string   `json:"requestId"`
	Selected   []string `json:"selected"`
	InputValue string   `json:"inputValue,omitempty"`
	Cancelled  bool     `json:"cancelled"`
}

// handleSessionChoiceResponse routes an incoming
// session/choice_response RPC into the pending registry. Returns an
// empty ack on successful delivery, an error when the request id is
// unknown (typically a late response after ctx cancellation).
//
// F10 ACP follow-on: when the chosen option has an input field with
// a validator, runs the validator BEFORE resolving. On validation
// failure returns an RPCError with the validator's message and
// leaves the entry in the registry — the client can correct the
// input and resend session/choice_response with the same requestId.
// Cancelled responses bypass validation (cancel always wins).
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

	// Validate input_value against the chosen option's validator
	// before resolving. Skip validation on cancelled responses;
	// also skip when the registry has no record of the original
	// request (shouldn't happen but harmless to fall through).
	if !p.Cancelled && len(p.Selected) == 1 {
		if req, ok := s.choiceRegistry.peek(p.RequestID); ok {
			for _, opt := range req.Options {
				if opt.ID != p.Selected[0] || opt.Input == nil || opt.Input.Validator == nil {
					continue
				}
				if err := pluginRuntime.ValidateChoiceInput(p.InputValue, opt.Input.Validator); err != nil {
					return nil, &RPCError{
						Code:    CodeInvalidParams,
						Message: "input validation failed: " + err.Error(),
					}
				}
				break
			}
		}
	}

	resp := pluginRuntime.ChoiceResponse{
		Selected:   p.Selected,
		InputValue: p.InputValue,
		Cancelled:  p.Cancelled,
	}
	if !s.choiceRegistry.resolve(p.RequestID, resp) {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "unknown requestId"}
	}
	return struct{}{}, nil
}
