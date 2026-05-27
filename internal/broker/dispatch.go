package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/sandbox"
)

// Method names handled by the broker dispatcher. All under the
// `broker.v1.*` namespace so future v2 schema evolution can be
// added alongside without breaking v1 clients.
const (
	MethodSessionCreate    = "broker.v1.session.create"
	MethodSessionTerminate = "broker.v1.session.terminate"
	MethodSessionTaint     = "broker.v1.session.taint"
	MethodToolRunSandbox   = "broker.v1.toolrun.sandbox"
	MethodPolicyQuery      = "broker.v1.policy.query"
)

// Error codes for broker.v1.* responses. Reserves -32020..-32029
// (next free band after stado-specific -32000..-32013 in
// internal/daemon/protocol.go).
const (
	ErrCodePolicyDeny        = -32020
	ErrCodeInvalidPurpose    = -32021
	ErrCodeInvalidProfile    = -32022
	ErrCodeSessionNotFound   = -32023
	ErrCodeSessionTerminated = -32024
	ErrCodePolicyLoad        = -32025
	ErrCodeInvalidParams     = -32026
	ErrCodeInternal          = -32027
)

// DispatchPrefix is the method-name prefix every broker.v1 method
// shares. The daemon's server.dispatch() switches on this prefix
// to route to Service.Dispatch().
const DispatchPrefix = "broker.v1."

// DispatchError carries the JSON-RPC error code + message returned
// to the client. The daemon wraps this into the protocol's Error
// envelope.
type DispatchError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *DispatchError) Error() string {
	return fmt.Sprintf("broker: %s (code %d)", e.Message, e.Code)
}

// Dispatch routes a broker.v1.* JSON-RPC method call to the
// appropriate handler. Returns the marshalled result on success;
// a *DispatchError on policy denial, validation failure, or
// session lookup failure; a generic error on broker-internal
// problems (rendered as ErrCodeInternal at the IPC layer).
func (s *Service) Dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if !strings.HasPrefix(method, DispatchPrefix) {
		return nil, &DispatchError{
			Code:    -32601, // method not found (re-using JSON-RPC standard)
			Message: "broker: unknown method: " + method,
		}
	}
	switch method {
	case MethodSessionCreate:
		return s.dispatchSessionCreate(ctx, params)
	case MethodSessionTerminate:
		return s.dispatchSessionTerminate(ctx, params)
	case MethodSessionTaint:
		return s.dispatchSessionTaint(ctx, params)
	case MethodToolRunSandbox:
		return s.dispatchToolRunSandbox(ctx, params)
	case MethodPolicyQuery:
		return s.dispatchPolicyQuery(ctx, params)
	}
	return nil, &DispatchError{
		Code:    -32601,
		Message: "broker: unknown method: " + method,
	}
}

// ──── Wire types ─────────────────────────────────────────────────

// SessionCreateParams is the wire shape for broker.v1.session.create.
// All fields not relevant to the declared purpose may be empty.
type SessionCreateParams struct {
	Purpose    Purpose  `json:"purpose"`
	Profile    Profile  `json:"profile"`
	CWD        string   `json:"cwd"`
	Role       string   `json:"role,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	WriteScope []string `json:"write_scope,omitempty"`
	PluginName string   `json:"plugin_name,omitempty"`
}

// SessionHandleResult is the wire shape for a SessionHandle in
// JSON-RPC responses. Distinct from the internal SessionHandle
// struct so the wire shape can evolve independently of the
// in-process type.
type SessionHandleResult struct {
	SessionID string         `json:"session_id"`
	Purpose   Purpose        `json:"purpose"`
	Ceiling   sandbox.Policy `json:"ceiling"`
	TraceRef  string         `json:"trace_ref,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Rule      string         `json:"rule"`
}

// SessionTerminateParams is the wire shape for broker.v1.session.terminate.
type SessionTerminateParams struct {
	SessionID string `json:"session_id"`
}

// SessionTerminateResult is the wire shape for a successful
// broker.v1.session.terminate response.
type SessionTerminateResult struct {
	OK bool `json:"ok"`
}

// ToolRunSandboxParams is the wire shape for broker.v1.toolrun.sandbox.
type ToolRunSandboxParams struct {
	PluginName     string          `json:"plugin_name"`
	ManifestDigest string          `json:"manifest_digest"`
	CWD            string          `json:"cwd"`
	PluginArgs     json.RawMessage `json:"plugin_args"`
}

// ToolRunSandboxResult is the wire shape for a successful
// broker.v1.toolrun.sandbox response. The handle is opaque in
// phase 1 (no actual sandbox is constructed yet — that's phase 2).
type ToolRunSandboxResult struct {
	SandboxHandle string         `json:"sandbox_handle"`
	Ceiling       sandbox.Policy `json:"ceiling"`
	Rule          string         `json:"rule"`
}

// PolicyQueryParams is the wire shape for broker.v1.policy.query.
type PolicyQueryParams struct {
	Request CapabilityRequest `json:"request"`
}

// PolicyQueryResult is the wire shape for a successful
// broker.v1.policy.query response.
type PolicyQueryResult struct {
	Decision Decision `json:"decision"`
}

// SessionTaintParams is the wire shape for broker.v1.session.taint.
// Used by ingestion sites to mark a session tainted (untrusted span
// entered the context) or to reset back to clean (operator-turn
// boundary).
type SessionTaintParams struct {
	SessionID string `json:"session_id"`
	Taint     string `json:"taint"` // "clean" | "tainted"
}

// SessionTaintResult is the wire shape for a successful
// broker.v1.session.taint response.
type SessionTaintResult struct {
	OK bool `json:"ok"`
}

// ──── Dispatch handlers ──────────────────────────────────────────

func (s *Service) dispatchSessionCreate(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionCreateParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.create: " + err.Error(),
		}
	}
	if !params.Purpose.Valid() {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidPurpose,
			Message: fmt.Sprintf("broker.v1.session.create: invalid purpose %q", params.Purpose),
		}
	}
	if !params.Profile.Valid() {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidProfile,
			Message: fmt.Sprintf("broker.v1.session.create: invalid profile %q", params.Profile),
		}
	}

	req := CapabilityRequest{
		Purpose:    params.Purpose,
		Profile:    params.Profile,
		CWD:        params.CWD,
		Role:       params.Role,
		Mode:       params.Mode,
		WriteScope: params.WriteScope,
		PluginName: params.PluginName,
	}

	handle, decision, err := s.CreateSession(req)
	if err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "broker.v1.session.create: " + err.Error(),
		}
	}
	if !decision.Admit {
		return nil, &DispatchError{
			Code:    ErrCodePolicyDeny,
			Message: "broker.v1.session.create: denied by " + decision.Rule,
		}
	}

	result := SessionHandleResult{
		SessionID: handle.SessionID,
		Purpose:   handle.Purpose,
		Ceiling:   handle.Ceiling,
		TraceRef:  handle.TraceRef,
		CreatedAt: handle.CreatedAt,
		Rule:      decision.Rule,
	}
	if !handle.ExpiresAt.IsZero() {
		result.ExpiresAt = &handle.ExpiresAt
	}
	return json.Marshal(result)
}

func (s *Service) dispatchSessionTerminate(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionTerminateParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.terminate: " + err.Error(),
		}
	}
	if params.SessionID == "" {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.terminate: session_id required",
		}
	}
	if err := s.TerminateSession(params.SessionID); err != nil {
		switch {
		case errors.Is(err, ErrSessionNotFound):
			return nil, &DispatchError{
				Code:    ErrCodeSessionNotFound,
				Message: "broker.v1.session.terminate: session not found",
			}
		case errors.Is(err, ErrSessionTerminated):
			return nil, &DispatchError{
				Code:    ErrCodeSessionTerminated,
				Message: "broker.v1.session.terminate: session already terminated",
			}
		}
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "broker.v1.session.terminate: " + err.Error(),
		}
	}
	return json.Marshal(SessionTerminateResult{OK: true})
}

func (s *Service) dispatchToolRunSandbox(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ToolRunSandboxParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.toolrun.sandbox: " + err.Error(),
		}
	}
	if params.PluginName == "" {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.toolrun.sandbox: plugin_name required",
		}
	}

	req := CapabilityRequest{
		Purpose:    PurposeToolRun,
		Profile:    ProfileDefault,
		CWD:        params.CWD,
		PluginName: params.PluginName,
	}
	decision := s.Evaluate(req)
	if !decision.Admit {
		return nil, &DispatchError{
			Code:    ErrCodePolicyDeny,
			Message: "broker.v1.toolrun.sandbox: denied by " + decision.Rule,
		}
	}

	// Phase 1: the sandbox handle is opaque and ephemeral. Phase 2
	// constructs an actual sandbox; for now we mint an ID so the
	// caller has something to correlate.
	id, err := mintSessionID()
	if err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "broker.v1.toolrun.sandbox: mint handle: " + err.Error(),
		}
	}
	return json.Marshal(ToolRunSandboxResult{
		SandboxHandle: id,
		Ceiling:       projectCeiling(req),
		Rule:          decision.Rule,
	})
}

func (s *Service) dispatchSessionTaint(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionTaintParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.taint: " + err.Error(),
		}
	}
	if params.SessionID == "" {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.taint: session_id required",
		}
	}
	var t Taint
	switch params.Taint {
	case "clean":
		t = TaintClean
	case "tainted":
		t = TaintTainted
	default:
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: fmt.Sprintf("broker.v1.session.taint: invalid taint %q (want 'clean' or 'tainted')", params.Taint),
		}
	}
	if err := s.SetTaint(params.SessionID, t); err != nil {
		switch {
		case errors.Is(err, ErrSessionNotFound):
			return nil, &DispatchError{
				Code:    ErrCodeSessionNotFound,
				Message: "broker.v1.session.taint: session not found",
			}
		case errors.Is(err, ErrSessionTerminated):
			return nil, &DispatchError{
				Code:    ErrCodeSessionTerminated,
				Message: "broker.v1.session.taint: session terminated",
			}
		}
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "broker.v1.session.taint: " + err.Error(),
		}
	}
	return json.Marshal(SessionTaintResult{OK: true})
}

func (s *Service) dispatchPolicyQuery(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params PolicyQueryParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.policy.query: " + err.Error(),
		}
	}
	decision := s.Evaluate(params.Request)
	return json.Marshal(PolicyQueryResult{Decision: decision})
}

// strictUnmarshal decodes raw into v with DisallowUnknownFields so
// the broker rejects clients that send a typo'd or extra field
// rather than silently ignoring it. The v1 IPC contract is strict
// by design.
func strictUnmarshal(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
