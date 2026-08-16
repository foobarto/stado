package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/sessioncontext"
)

const (
	maxTrajectoryCallIDRunes = 256
	maxTrajectoryToolRunes   = 256
)

var errTrajectoryDurableSession = errors.New("broker: session context requires a durable logical session")

func (s *Service) dispatchSessionContextObjective(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionContextObjectiveParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidTrajectoryParams(MethodSessionContextObjective, err)
	}
	svc, subject, principal, err := s.authenticatedSessionContext(params.SessionID, params.ControllerToken)
	if err != nil {
		return nil, trajectoryAuthError(MethodSessionContextObjective, err)
	}
	if strings.TrimSpace(params.Objective) == "" || utf8.RuneCountInString(strings.TrimSpace(params.Objective)) > 4096 {
		return nil, invalidTrajectoryParams(MethodSessionContextObjective, errors.New("trajectory objective is required and bounded"))
	}
	if _, err := svc.EnsureObjective(ctx, subject, params.Objective, principal, "broker:trajectory", "objective:"+subject); err != nil {
		return nil, trajectoryStoreError(MethodSessionContextObjective, err)
	}
	return json.Marshal(SessionContextWriteResult{OK: true})
}

func (s *Service) dispatchSessionContextToolOutcome(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params SessionContextToolOutcomeParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidTrajectoryParams(MethodSessionContextToolOutcome, err)
	}
	svc, subject, principal, err := s.authenticatedSessionContext(params.SessionID, params.ControllerToken)
	if err != nil {
		return nil, trajectoryAuthError(MethodSessionContextToolOutcome, err)
	}
	if err := validateSessionContextToolOutcome(params); err != nil {
		return nil, invalidTrajectoryParams(MethodSessionContextToolOutcome, err)
	}
	kind := sessioncontext.ObservationTool
	attributes := map[string]string{}
	if params.Denied {
		kind = sessioncontext.ObservationDenial
		attributes["boundary"] = params.Tool
	}
	callDigest := sha256.Sum256([]byte(params.CallID))
	callRef := hex.EncodeToString(callDigest[:])
	obs := sessioncontext.Observation{
		SessionID:   subject,
		Kind:        kind,
		Tool:        params.Tool,
		ArgsDigest:  params.ArgsDigest,
		Succeeded:   params.Succeeded,
		EvidenceRef: fmt.Sprintf("session:%s/turn:%d/tool-call:%s", subject, params.Turn, callRef),
		Attributes:  attributes,
	}
	idem := fmt.Sprintf("trajectory:%s:%d:%s", subject, params.Turn, callRef)
	if _, err := svc.Observe(ctx, obs, principal, "broker:trajectory", idem); err != nil {
		return nil, trajectoryStoreError(MethodSessionContextToolOutcome, err)
	}
	return json.Marshal(SessionContextWriteResult{OK: true})
}

func (s *Service) authenticatedSessionContext(sessionID, controllerToken string) (*sessioncontext.Service, string, string, error) {
	if s == nil || s.sessionContext == nil {
		return nil, "", "", errors.New("broker: session context authority unavailable")
	}
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return nil, "", "", ErrSessionNotFound
	}
	if state.terminated {
		return nil, "", "", ErrSessionTerminated
	}
	if err := authenticateControllerLocked(state, controllerToken); err != nil {
		return nil, "", "", err
	}
	if !state.scope.durable || strings.TrimSpace(state.scope.subject) == "" {
		return nil, "", "", errTrajectoryDurableSession
	}
	return s.sessionContext, state.scope.subject, state.principal, nil
}

func invalidTrajectoryParams(method string, err error) *DispatchError {
	return &DispatchError{Code: ErrCodeInvalidParams, Message: method + ": " + err.Error()}
}

func trajectoryAuthError(method string, err error) *DispatchError {
	code := ErrCodeInvalidParams
	if errors.Is(err, ErrSessionNotFound) {
		code = ErrCodeSessionNotFound
	} else if errors.Is(err, ErrSessionTerminated) {
		code = ErrCodeSessionTerminated
	} else if !errors.Is(err, ErrSessionController) && !errors.Is(err, errTrajectoryDurableSession) {
		code = ErrCodeInternal
	}
	return &DispatchError{Code: code, Message: method + ": " + err.Error()}
}

func trajectoryStoreError(method string, err error) *DispatchError {
	return &DispatchError{Code: ErrCodeInternal, Message: method + ": canonical session context: " + err.Error()}
}

func validateSessionContextToolOutcome(params SessionContextToolOutcomeParams) error {
	if params.Turn < 0 {
		return errors.New("trajectory turn must be non-negative")
	}
	if strings.TrimSpace(params.CallID) == "" || utf8.RuneCountInString(params.CallID) > maxTrajectoryCallIDRunes {
		return errors.New("trajectory call_id is required and bounded")
	}
	if strings.TrimSpace(params.Tool) == "" || utf8.RuneCountInString(params.Tool) > maxTrajectoryToolRunes {
		return errors.New("trajectory tool is required and bounded")
	}
	if len(params.ArgsDigest) != sha256.Size*2 {
		return errors.New("trajectory args_digest must be a SHA-256 hex digest")
	}
	decoded, err := hex.DecodeString(params.ArgsDigest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(params.ArgsDigest) != params.ArgsDigest {
		return errors.New("trajectory args_digest must be lowercase SHA-256 hex")
	}
	if params.Denied && params.Succeeded {
		return errors.New("trajectory denial cannot be successful")
	}
	return nil
}
