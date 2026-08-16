package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/sandbox"
)

// Method names handled by the broker dispatcher. All under the
// `broker.v1.*` namespace so future v2 schema evolution can be
// added alongside without breaking v1 clients.
const (
	MethodSessionCreate                   = "broker.v1.session.create"
	MethodSessionTerminate                = "broker.v1.session.terminate"
	MethodSessionTaint                    = "broker.v1.session.taint"
	MethodSessionContextObjective         = "broker.v1.session.context.objective"
	MethodSessionContextToolOutcome       = "broker.v1.session.context.tool_outcome"
	MethodToolRunSandbox                  = "broker.v1.toolrun.sandbox"
	MethodPolicyQuery                     = "broker.v1.policy.query"
	MethodApplicationBind                 = "broker.v1.application.bind"
	MethodApplicationCall                 = "broker.v1.application.call"
	MethodApplicationEventsNext           = "broker.v1.application.events.next"
	MethodApplicationEventsAck            = "broker.v1.application.events.ack"
	MethodApplicationEventPublish         = "broker.v1.application.event.publish"
	MethodApplicationInputCapture         = "broker.v1.application.input.capture"
	MethodApplicationInputState           = "broker.v1.application.input.state"
	MethodApplicationInputCommit          = "broker.v1.application.input.commit"
	MethodApplicationContinuationCommit   = "broker.v1.application.continuation.commit"
	MethodApplicationWorkerGet            = "broker.v1.application.worker.get"
	MethodApplicationWorkerActivate       = "broker.v1.application.worker.activate"
	MethodApplicationWorkerResumeActivate = "broker.v1.application.worker.resume.activate"
	MethodApplicationWorkerCancel         = "broker.v1.application.worker.cancel"
	MethodApplicationVerificationGet      = "broker.v1.application.verification.get"
	MethodApplicationVerificationClaim    = "broker.v1.application.verification.claim"
	MethodApplicationVerificationFinish   = "broker.v1.application.verification.finish"
	MethodSessionSchedule                 = "broker.v1.session.schedule"
	MethodSessionScheduleConsume          = "broker.v1.session.schedule.consume"
	MethodArtifactBind                    = "broker.v1.artifact.bind"
	MethodArtifactPropose                 = "broker.v1.artifact.propose"
	MethodArtifactQuery                   = "broker.v1.artifact.query"
	MethodArtifactEdit                    = "broker.v1.artifact.edit"
	MethodArtifactObserve                 = "broker.v1.artifact.observe"
	MethodEvidenceBind                    = "broker.v1.evidence.bind"
	MethodEvidenceCall                    = "broker.v1.evidence.call"
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
	case MethodSessionAdopt:
		return s.dispatchSessionAdopt(ctx, params)
	case MethodSessionReserve:
		return s.dispatchSessionReserve(ctx, params)
	case MethodSessionDetach:
		return s.dispatchSessionDetach(ctx, params)
	case MethodSessionHeartbeat:
		return s.dispatchSessionHeartbeat(ctx, params)
	case MethodSessionHandoffReserve:
		return s.dispatchSessionHandoffReserve(ctx, params)
	case MethodSessionHandoffCommit:
		return s.dispatchSessionHandoffCommit(ctx, params)
	case MethodSessionTerminate:
		return s.dispatchSessionTerminate(ctx, params)
	case MethodSessionTaint:
		return s.dispatchSessionTaint(ctx, params)
	case MethodSessionContextObjective:
		return s.dispatchSessionContextObjective(ctx, params)
	case MethodSessionContextToolOutcome:
		return s.dispatchSessionContextToolOutcome(ctx, params)
	case MethodToolRunSandbox:
		return s.dispatchToolRunSandbox(ctx, params)
	case MethodPolicyQuery:
		return s.dispatchPolicyQuery(ctx, params)
	case MethodApplicationBind:
		return s.dispatchApplicationBind(ctx, params)
	case MethodApplicationCall:
		return s.dispatchApplicationCall(ctx, params)
	case MethodApplicationEventsNext:
		return s.dispatchApplicationEventsNext(ctx, params)
	case MethodApplicationEventsAck:
		return s.dispatchApplicationEventsAck(ctx, params)
	case MethodApplicationEventPublish:
		return s.dispatchApplicationEventPublish(ctx, params)
	case MethodApplicationInputCapture:
		return dispatchStrict(ctx, s, params, MethodApplicationInputCapture, (*Service).applicationInputCapture)
	case MethodApplicationInputState:
		return dispatchStrict(ctx, s, params, MethodApplicationInputState, (*Service).applicationInputState)
	case MethodApplicationInputCommit:
		return dispatchStrict(ctx, s, params, MethodApplicationInputCommit, (*Service).applicationInputCommit)
	case MethodApplicationContinuationCommit:
		return dispatchStrict(ctx, s, params, MethodApplicationContinuationCommit, (*Service).applicationContinuationCommit)
	case MethodApplicationWorkerGet:
		return dispatchStrict(ctx, s, params, MethodApplicationWorkerGet, (*Service).applicationWorkerGet)
	case MethodApplicationWorkerActivate:
		return dispatchStrict(ctx, s, params, MethodApplicationWorkerActivate, (*Service).applicationWorkerActivate)
	case MethodApplicationWorkerResumeActivate:
		return dispatchStrict(ctx, s, params, MethodApplicationWorkerResumeActivate, (*Service).applicationWorkerResumeActivate)
	case MethodApplicationWorkerCancel:
		return dispatchStrict(ctx, s, params, MethodApplicationWorkerCancel, (*Service).applicationWorkerCancel)
	case MethodApplicationVerificationGet:
		return dispatchStrict(ctx, s, params, MethodApplicationVerificationGet, (*Service).applicationVerificationGet)
	case MethodApplicationVerificationClaim:
		return dispatchStrict(ctx, s, params, MethodApplicationVerificationClaim, (*Service).applicationVerificationClaim)
	case MethodApplicationVerificationFinish:
		return dispatchStrict(ctx, s, params, MethodApplicationVerificationFinish, (*Service).applicationVerificationFinish)
	case MethodSessionSchedule:
		return s.dispatchSessionSchedule(ctx, params)
	case MethodSessionScheduleConsume:
		return s.dispatchSessionScheduleConsume(ctx, params)
	case MethodArtifactBind:
		return s.dispatchArtifactBind(ctx, params)
	case MethodArtifactPropose:
		return s.dispatchArtifactCall(ctx, params, s.artifactPropose)
	case MethodArtifactQuery:
		return s.dispatchArtifactCall(ctx, params, s.artifactQuery)
	case MethodArtifactEdit:
		return s.dispatchArtifactCall(ctx, params, s.artifactEdit)
	case MethodArtifactObserve:
		return s.dispatchArtifactCall(ctx, params, s.artifactObserve)
	case MethodEvidenceBind:
		return s.dispatchEvidenceBind(ctx, params)
	case MethodEvidenceCall:
		return s.dispatchEvidenceCall(ctx, params)
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
	Purpose               Purpose  `json:"purpose"`
	Profile               Profile  `json:"profile"`
	CWD                   string   `json:"cwd"`
	ParentSessionID       string   `json:"parent_session_id,omitempty"`
	ParentControllerToken string   `json:"parent_controller_token,omitempty"`
	Role                  string   `json:"role,omitempty"`
	Mode                  string   `json:"mode,omitempty"`
	WriteScope            []string `json:"write_scope,omitempty"`
	PluginName            string   `json:"plugin_name,omitempty"`
	// Subject opts a main-chat peer into durable logical-session adoption. It
	// is an exact broker-recorded git session identity, never guest authority.
	Subject        string `json:"subject,omitempty"`
	AdoptionTicket string `json:"adoption_ticket,omitempty"`
	ResumeSecret   string `json:"resume_secret,omitempty"`
}

// SessionHandleResult is the wire shape for a SessionHandle in
// JSON-RPC responses. Distinct from the internal SessionHandle
// struct so the wire shape can evolve independently of the
// in-process type.
type SessionHandleResult struct {
	SessionID       string         `json:"session_id"`
	ControllerToken string         `json:"controller_token"`
	Purpose         Purpose        `json:"purpose"`
	CWD             string         `json:"cwd"`
	Ceiling         sandbox.Policy `json:"ceiling"`
	TraceRef        string         `json:"trace_ref,omitempty"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	Rule            string         `json:"rule"`
	Subject         string         `json:"subject,omitempty"`
	AdoptionTicket  string         `json:"adoption_ticket,omitempty"`
	ResumeSecret    string         `json:"resume_secret,omitempty"`
}

// SessionTerminateParams is the wire shape for broker.v1.session.terminate.
type SessionTerminateParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
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
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	Taint           string `json:"taint"` // "clean" | "tainted"
}

// SessionTaintResult is the wire shape for a successful
// broker.v1.session.taint response.
type SessionTaintResult struct {
	OK bool `json:"ok"`
}

// SessionContextObjectiveParams is native controller input. The broker derives
// the durable logical subject and principal from the authenticated session;
// neither can be selected by the client.
type SessionContextObjectiveParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	Objective       string `json:"objective"`
}

// SessionContextToolOutcomeParams carries bounded mechanical facts observed by
// the native runtime. Evidence refs, actor, principal, and idempotency are
// broker-authored from the admitted durable session.
type SessionContextToolOutcomeParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	Turn            int    `json:"turn"`
	CallID          string `json:"call_id"`
	Tool            string `json:"tool"`
	ArgsDigest      string `json:"args_digest"`
	Succeeded       bool   `json:"succeeded"`
	Denied          bool   `json:"denied,omitempty"`
}

type SessionContextWriteResult struct {
	OK bool `json:"ok"`
}

// ArtifactBindParams is sent only by the native verified plugin loader. The
// returned token is withheld from WASM linear memory; guest payloads reach the
// broker through the in-process host bridge that owns it.
type ArtifactBindParams struct {
	SessionID       string                  `json:"session_id"`
	ControllerToken string                  `json:"controller_token"`
	Identity        plugins.RuntimeIdentity `json:"identity"`
	Manifest        plugins.Manifest        `json:"manifest"`
	// ToolName selects one exact signed ToolDef. Ordinary artifact/evidence
	// tokens are attenuated to that tool's effective capability subset.
	ToolName string `json:"tool_name"`
}

type ArtifactBindResult struct {
	BindingToken       string   `json:"binding_token"`
	Principal          string   `json:"principal"`
	CanonicalRepoID    string   `json:"canonical_repo_id,omitempty"`
	SessionID          string   `json:"session_id"`
	SessionGeneration  uint64   `json:"session_generation"`
	AncestorSessionIDs []string `json:"ancestor_session_ids,omitempty"`
}

// ApplicationBindParams is deliberately distinct from ArtifactBindParams and
// has no tool selector. A lifecycle application binds its complete signed
// application authority; strict RPC decoding rejects a supplied tool_name.
// An admitted lifecycle application owns a durable event cursor, so the broker
// allows only one live binding per session generation and plugin namespace.
type ApplicationBindParams struct {
	SessionID       string                  `json:"session_id"`
	ControllerToken string                  `json:"controller_token"`
	Identity        plugins.RuntimeIdentity `json:"identity"`
	Manifest        plugins.Manifest        `json:"manifest"`
}
type ApplicationBindResult = ArtifactBindResult

// ArtifactCallParams carries only an opaque native-held binding plus the
// bounded guest request. Scope, principal, identity, actor, and ancestry are
// resolved from broker state and cannot be supplied here.
type ArtifactCallParams struct {
	BindingToken string          `json:"binding_token"`
	RequestID    string          `json:"request_id"`
	Payload      json.RawMessage `json:"payload"`
}

type EvidenceBindParams ArtifactBindParams

// EvidenceCallParams carries only a native-held broker binding and one fixed
// host-selected operation. Corpus selectors in Payload are non-authoritative;
// the broker resolves repository/session/plugin scope from the binding.
type EvidenceCallParams struct {
	BindingToken string          `json:"binding_token"`
	Operation    string          `json:"operation"`
	Payload      json.RawMessage `json:"payload"`
}

// ApplicationCallParams carries one fixed host-selected operation under the
// opaque admission token. Guest JSON is only Payload; identity, scope,
// capability set, actor, and idempotency namespace come from broker state.
type ApplicationCallParams struct {
	BindingToken string          `json:"binding_token"`
	RequestID    string          `json:"request_id"`
	Operation    string          `json:"operation"`
	Payload      json.RawMessage `json:"payload"`
}

type ApplicationEventsNextParams struct {
	BindingToken string `json:"binding_token"`
	Limit        int    `json:"limit,omitempty"`
}

type ApplicationEventsAckParams struct {
	BindingToken string `json:"binding_token"`
	RequestID    string `json:"request_id"`
	Sequence     uint64 `json:"sequence"`
}

type ApplicationEventResult struct {
	Kind         string          `json:"kind"`
	WALSequence  uint64          `json:"wal_sequence"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Data         json.RawMessage `json:"data"`
}

// ApplicationEventPublishParams is accepted only on the native broker client
// surface. There is no WASM host import for it; the broker supplies generation,
// ordering, timestamp, and publisher metadata.
type ApplicationEventPublishParams struct {
	SessionID          string          `json:"session_id"`
	ControllerToken    string          `json:"controller_token"`
	ExpectedGeneration uint64          `json:"expected_generation,omitempty"`
	RequestID          string          `json:"request_id"`
	ID                 string          `json:"id,omitempty"`
	Kind               string          `json:"kind"`
	Data               json.RawMessage `json:"data"`
	EvidenceRefs       []string        `json:"evidence_refs,omitempty"`
}

type SessionScheduleParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
}

// SessionScheduleConsumeParams is native controller input. Sequence must name
// the exact pause/stop result about to end recurrence; the broker derives the
// application run, plugin authority, outcome, and durable idempotency key.
type SessionScheduleConsumeParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	Sequence        uint64 `json:"sequence"`
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
	if params.ParentSessionID != "" || params.ParentControllerToken != "" || params.Purpose == PurposeSubagent {
		if params.ParentSessionID == "" || params.ParentControllerToken == "" {
			return nil, &DispatchError{
				Code: ErrCodeInvalidParams, Message: "broker.v1.session.create: parent_session_id and parent_controller_token are required together",
			}
		}
		if err := s.authenticateSessionController(params.ParentSessionID, params.ParentControllerToken); err != nil {
			return nil, invalidArtifactParams(MethodSessionCreate, err)
		}
	}
	if params.Subject != "" && (params.AdoptionTicket == "" || params.ResumeSecret == "") {
		return nil, &DispatchError{Code: ErrCodeInvalidParams, Message: "broker.v1.session.create: durable subject requires a pre-staged adoption credential"}
	}

	req := CapabilityRequest{
		Purpose:    params.Purpose,
		Profile:    params.Profile,
		CWD:        params.CWD,
		Role:       params.Role,
		Mode:       params.Mode,
		WriteScope: params.WriteScope,
		PluginName: params.PluginName,
		SessionID:  params.ParentSessionID,
	}

	var handle SessionHandle
	var decision Decision
	var err error
	if params.Subject != "" {
		handle, decision, err = s.CreateSessionForCredential(req, SessionAdoptionCredential{
			Subject: params.Subject, Ticket: params.AdoptionTicket, ResumeSecret: params.ResumeSecret,
		})
	} else {
		handle, decision, err = s.CreateSession(req)
	}
	if err != nil {
		if params.Subject != "" {
			return nil, sessionScopeDispatchError(MethodSessionCreate, err)
		}
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
		SessionID:       handle.SessionID,
		ControllerToken: handle.controllerToken,
		Purpose:         handle.Purpose,
		CWD:             handle.CWD,
		Ceiling:         handle.Ceiling,
		TraceRef:        handle.TraceRef,
		CreatedAt:       handle.CreatedAt,
		Rule:            decision.Rule,
		Subject:         handle.subject,
		AdoptionTicket:  handle.adoptionTicket,
		ResumeSecret:    handle.resumeSecret,
	}
	if !handle.ExpiresAt.IsZero() {
		result.ExpiresAt = &handle.ExpiresAt
	}
	return json.Marshal(result)
}

func (s *Service) dispatchArtifactBind(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ArtifactBindParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodArtifactBind, err)
	}
	result, err := s.bindArtifacts(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodArtifactBind, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchEvidenceBind(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params EvidenceBindParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodEvidenceBind, err)
	}
	result, err := s.bindEvidence(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodEvidenceBind, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchEvidenceCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params EvidenceCallParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodEvidenceCall, err)
	}
	result, err := s.evidenceCall(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodEvidenceCall, err)
	}
	return result, nil
}

func (s *Service) dispatchApplicationBind(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ApplicationBindParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodApplicationBind, err)
	}
	result, err := s.bindApplication(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodApplicationBind, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchApplicationCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ApplicationCallParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodApplicationCall, err)
	}
	result, err := s.applicationCall(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodApplicationCall, err)
	}
	return result, nil
}

func (s *Service) dispatchApplicationEventsNext(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ApplicationEventsNextParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodApplicationEventsNext, err)
	}
	result, err := s.applicationEventsNext(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodApplicationEventsNext, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchApplicationEventsAck(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ApplicationEventsAckParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodApplicationEventsAck, err)
	}
	result, err := s.applicationEventsAck(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodApplicationEventsAck, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchApplicationEventPublish(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params ApplicationEventPublishParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodApplicationEventPublish, err)
	}
	result, err := s.applicationEventPublish(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodApplicationEventPublish, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchSessionSchedule(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionScheduleParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodSessionSchedule, err)
	}
	result, err := s.sessionSchedule(ctx, params.SessionID, params.ControllerToken)
	if err != nil {
		return nil, invalidArtifactParams(MethodSessionSchedule, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchSessionScheduleConsume(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionScheduleConsumeParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodSessionScheduleConsume, err)
	}
	result, err := s.sessionScheduleConsume(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodSessionScheduleConsume, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchArtifactCall(ctx context.Context, raw json.RawMessage, call func(context.Context, ArtifactCallParams) (json.RawMessage, error)) (json.RawMessage, error) {
	var params ArtifactCallParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams("broker.v1.artifact", err)
	}
	result, err := call(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams("broker.v1.artifact", err)
	}
	return result, nil
}

func invalidArtifactParams(method string, err error) *DispatchError {
	code := ErrCodeInvalidParams
	if errors.Is(err, ErrSessionNotFound) {
		code = ErrCodeSessionNotFound
	} else if errors.Is(err, ErrSessionTerminated) {
		code = ErrCodeSessionTerminated
	}
	return &DispatchError{Code: code, Message: method + ": " + err.Error()}
}

func (s *Service) dispatchSessionTerminate(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionTerminateParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.terminate: " + err.Error(),
		}
	}
	if params.SessionID == "" || params.ControllerToken == "" {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.terminate: session_id and controller_token required",
		}
	}
	if err := s.TerminateSession(params.SessionID, params.ControllerToken); err != nil {
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
		case errors.Is(err, ErrSessionController):
			return nil, &DispatchError{
				Code: ErrCodeInvalidParams, Message: "broker.v1.session.terminate: " + err.Error(),
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
	if params.SessionID == "" || params.ControllerToken == "" {
		return nil, &DispatchError{
			Code:    ErrCodeInvalidParams,
			Message: "broker.v1.session.taint: session_id and controller_token required",
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
	if err := s.SetTaint(params.SessionID, params.ControllerToken, t); err != nil {
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
		case errors.Is(err, ErrSessionController):
			return nil, &DispatchError{
				Code: ErrCodeInvalidParams, Message: "broker.v1.session.taint: " + err.Error(),
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
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON after request object")
	}
	return nil
}
