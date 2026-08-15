package broker

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	MethodSessionAdopt          = "broker.v1.session.adopt"
	MethodSessionReserve        = "broker.v1.session.reserve"
	MethodSessionDetach         = "broker.v1.session.detach"
	MethodSessionHeartbeat      = "broker.v1.session.heartbeat"
	MethodSessionHandoffReserve = "broker.v1.session.handoff.reserve"
	MethodSessionHandoffCommit  = "broker.v1.session.handoff.commit"

	ErrCodeSessionScopeActive     = -32028
	ErrCodeSessionScopeCredential = -32029
	ErrCodeSessionHandoffConflict = -32030
)

type SessionAdoptParams struct {
	Subject      string `json:"subject"`
	Ticket       string `json:"ticket"`
	ResumeSecret string `json:"resume_secret"`
	CWD          string `json:"cwd"`
}

type SessionReserveParams struct {
	ParentSessionID       string `json:"parent_session_id"`
	ParentControllerToken string `json:"parent_controller_token"`
	Subject               string `json:"subject"`
	CWD                   string `json:"cwd"`
}

type SessionReserveResult struct {
	Subject      string    `json:"subject"`
	Ticket       string    `json:"ticket"`
	ResumeSecret string    `json:"resume_secret"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type SessionDetachParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
}

type SessionHeartbeatParams = SessionDetachParams

type SessionScopeResult struct {
	OK bool `json:"ok"`
}

type SessionHandoffReserveParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	ChildSubject    string `json:"child_subject"`
	SourceTurnRef   string `json:"source_turn_ref"`
}

type SessionHandoffCommitParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	HandoffID       string `json:"handoff_id"`
	ChildSubject    string `json:"child_subject"`
	Ticket          string `json:"ticket"`
	ResumeSecret    string `json:"resume_secret"`
}

func (s *Service) dispatchSessionAdopt(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionAdoptParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionAdopt, err)
	}
	handle, credential, err := s.AdoptSession(SessionAdoptionCredential{
		Subject: params.Subject, Ticket: params.Ticket, ResumeSecret: params.ResumeSecret,
	}, params.CWD)
	if err != nil {
		return nil, sessionScopeDispatchError(MethodSessionAdopt, err)
	}
	result := SessionHandleResult{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		Purpose: handle.Purpose, CWD: handle.CWD, Ceiling: handle.Ceiling,
		TraceRef: handle.TraceRef, CreatedAt: handle.CreatedAt,
		Subject: credential.Subject, AdoptionTicket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	}
	if !handle.ExpiresAt.IsZero() {
		result.ExpiresAt = &handle.ExpiresAt
	}
	return json.Marshal(result)
}

func (s *Service) dispatchSessionReserve(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionReserveParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionReserve, err)
	}
	credential, expiresAt, err := s.ReserveSessionScope(
		params.ParentSessionID, params.ParentControllerToken, params.Subject, params.CWD,
	)
	if err != nil {
		return nil, sessionScopeDispatchError(MethodSessionReserve, err)
	}
	return json.Marshal(SessionReserveResult{
		Subject: credential.Subject, Ticket: credential.Ticket,
		ResumeSecret: credential.ResumeSecret, ExpiresAt: expiresAt,
	})
}

func (s *Service) dispatchSessionDetach(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionDetachParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionDetach, err)
	}
	if params.SessionID == "" || params.ControllerToken == "" {
		return nil, sessionScopeDispatchError(MethodSessionDetach, ErrSessionController)
	}
	if err := s.DetachSession(params.SessionID, params.ControllerToken); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionDetach, err)
	}
	return json.Marshal(SessionScopeResult{OK: true})
}

func (s *Service) dispatchSessionHeartbeat(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionHeartbeatParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionHeartbeat, err)
	}
	if params.SessionID == "" || params.ControllerToken == "" {
		return nil, sessionScopeDispatchError(MethodSessionHeartbeat, ErrSessionController)
	}
	if err := s.HeartbeatSession(params.SessionID, params.ControllerToken); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionHeartbeat, err)
	}
	return json.Marshal(SessionScopeResult{OK: true})
}

func (s *Service) dispatchSessionHandoffReserve(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionHandoffReserveParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionHandoffReserve, err)
	}
	reservation, err := s.ReserveSessionSubjectHandoff(
		ctx, params.SessionID, params.ControllerToken, params.ChildSubject, params.SourceTurnRef,
	)
	if err != nil {
		return nil, sessionScopeDispatchError(MethodSessionHandoffReserve, err)
	}
	return json.Marshal(reservation)
}

func (s *Service) dispatchSessionHandoffCommit(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionHandoffCommitParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, sessionScopeDispatchError(MethodSessionHandoffCommit, err)
	}
	handle, credential, err := s.CommitSessionSubjectHandoff(ctx, params.SessionID, params.ControllerToken, params.HandoffID, SessionAdoptionCredential{
		Subject: params.ChildSubject, Ticket: params.Ticket, ResumeSecret: params.ResumeSecret,
	})
	if err != nil {
		return nil, sessionScopeDispatchError(MethodSessionHandoffCommit, err)
	}
	result := SessionHandleResult{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		Purpose: handle.Purpose, CWD: handle.CWD, Ceiling: handle.Ceiling,
		TraceRef: handle.TraceRef, CreatedAt: handle.CreatedAt,
		Subject: credential.Subject, AdoptionTicket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	}
	if !handle.ExpiresAt.IsZero() {
		result.ExpiresAt = &handle.ExpiresAt
	}
	return json.Marshal(result)
}

func sessionScopeDispatchError(method string, err error) *DispatchError {
	code := ErrCodeInvalidParams
	switch {
	case errors.Is(err, ErrSessionNotFound):
		code = ErrCodeSessionNotFound
	case errors.Is(err, ErrSessionTerminated):
		code = ErrCodeSessionTerminated
	case errors.Is(err, ErrSessionScopeActive):
		code = ErrCodeSessionScopeActive
	case errors.Is(err, ErrSessionScopeCredential):
		code = ErrCodeSessionScopeCredential
	case errors.Is(err, ErrSessionHandoffConflict):
		code = ErrCodeSessionHandoffConflict
	case errors.Is(err, ErrSessionHandoffUnavailable):
		code = ErrCodeInternal
	case errors.Is(err, ErrSessionScopeUnavailable):
		code = ErrCodeInternal
	}
	return &DispatchError{Code: code, Message: method + ": " + err.Error()}
}
