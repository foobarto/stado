package broker

// Native controller RPC for broker-owned operator input. These methods bind
// the current controller token and exact session generation before touching
// application state. No WASM import exposes capture, delivery, recovery, or
// continuation commit.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/foobarto/stado/internal/broker/application"
)

func dispatchStrict[P any, R any](ctx context.Context, service *Service, raw json.RawMessage, method string, call func(*Service, context.Context, P) (R, error)) (json.RawMessage, error) {
	var params P
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(method, err)
	}
	result, err := call(service, ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(method, err)
	}
	return json.Marshal(result)
}

type ApplicationInputCaptureParams struct {
	SessionID          string `json:"session_id"`
	ControllerToken    string `json:"controller_token"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	RequestID          string `json:"request_id"`
	Text               string `json:"text"`
}

type ApplicationInputStateParams struct {
	SessionID          string `json:"session_id"`
	ControllerToken    string `json:"controller_token"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

type ApplicationInputCommitParams struct {
	SessionID          string `json:"session_id"`
	ControllerToken    string `json:"controller_token"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	RequestID          string `json:"request_id"`
	InputID            string `json:"input_id"`
	ExpectedVersion    uint64 `json:"expected_version"`
	Recovery           bool   `json:"recovery,omitempty"`
}

type ApplicationContinuationCommitParams struct {
	SessionID          string `json:"session_id"`
	ControllerToken    string `json:"controller_token"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	RequestID          string `json:"request_id"`
	CompletionID       string `json:"completion_id"`
	RunID              string `json:"run_id"`
	DeliveryID         string `json:"delivery_id"`
}

type ApplicationInputStateResult struct {
	AsOfSequence              uint64                            `json:"as_of_sequence"`
	ActiveWorkerRun           *application.WorkerRun            `json:"active_worker_run,omitempty"`
	ReadyInputs               []application.OperatorInput       `json:"ready_inputs"`
	ReviewingInputs           []application.OperatorInput       `json:"reviewing_inputs"`
	RecoveryInputs            []application.OperatorInput       `json:"recovery_inputs"`
	OpenDeferredTaskCount     int                               `json:"open_deferred_task_count,omitempty"`
	OpenDeferredTasks         []application.DeferredTaskSummary `json:"open_deferred_tasks"`
	OpenDeferredTruncated     bool                              `json:"open_deferred_tasks_truncated,omitempty"`
	PendingContinuation       *application.Continuation         `json:"pending_continuation,omitempty"`
	PendingContinuationInputs []application.ContinuationInput   `json:"pending_continuation_inputs,omitempty"`
}

func (s *Service) applicationInputScope(sessionID, controllerToken string, expectedGeneration uint64) (application.SessionScope, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(controllerToken) == "" || expectedGeneration == 0 {
		return application.SessionScope{}, errors.New("session_id, controller_token, and expected_generation are required")
	}
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return application.SessionScope{}, ErrSessionNotFound
	}
	if session.terminated {
		return application.SessionScope{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, controllerToken); err != nil {
		return application.SessionScope{}, err
	}
	if session.generation != expectedGeneration {
		return application.SessionScope{}, errors.New("application input session generation changed")
	}
	return application.SessionScope{SessionID: sessionID, Generation: session.generation}, nil
}

func (s *Service) applicationInputCapture(ctx context.Context, params ApplicationInputCaptureParams) (application.OperatorInput, error) {
	if strings.TrimSpace(params.RequestID) == "" {
		return application.OperatorInput{}, errors.New("application input request_id is required")
	}
	scope, err := s.applicationInputScope(params.SessionID, params.ControllerToken, params.ExpectedGeneration)
	if err != nil {
		return application.OperatorInput{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.OperatorInput{}, errors.New("application state authority unavailable")
	}
	return s.artifacts.application.CaptureOperatorInput(ctx, scope, params.Text, params.RequestID)
}

func (s *Service) applicationInputState(ctx context.Context, params ApplicationInputStateParams) (ApplicationInputStateResult, error) {
	scope, err := s.applicationInputScope(params.SessionID, params.ControllerToken, params.ExpectedGeneration)
	if err != nil {
		return ApplicationInputStateResult{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return ApplicationInputStateResult{}, errors.New("application state authority unavailable")
	}
	projection, err := s.artifacts.application.ProjectEnforcement(ctx, scope)
	if err != nil {
		return ApplicationInputStateResult{}, err
	}
	result := ApplicationInputStateResult{
		AsOfSequence: projection.AsOfSequence, ActiveWorkerRun: projection.ActiveWorkerRun,
		ReadyInputs: projection.ReadyOperatorInputs, ReviewingInputs: projection.ReviewingOperatorInputs,
		RecoveryInputs:        projection.RecoveryOperatorInputs,
		OpenDeferredTaskCount: projection.OpenDeferredTaskCount,
		OpenDeferredTasks:     projection.OpenDeferredTasks, OpenDeferredTruncated: projection.OpenDeferredTruncated,
		PendingContinuation: projection.PendingContinuation,
	}
	if result.PendingContinuation != nil {
		result.PendingContinuationInputs, err = s.artifacts.application.PendingContinuationInputs(ctx, scope, result.PendingContinuation.CompletionID)
		if err != nil {
			return ApplicationInputStateResult{}, err
		}
	}
	return result, nil
}

func (s *Service) applicationInputCommit(ctx context.Context, params ApplicationInputCommitParams) (application.OperatorInput, error) {
	if strings.TrimSpace(params.RequestID) == "" {
		return application.OperatorInput{}, errors.New("application input request_id is required")
	}
	scope, err := s.applicationInputScope(params.SessionID, params.ControllerToken, params.ExpectedGeneration)
	if err != nil {
		return application.OperatorInput{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.OperatorInput{}, errors.New("application state authority unavailable")
	}
	return s.artifacts.application.CommitOperatorInput(ctx, scope, application.OperatorInputCommit{
		InputID: params.InputID, ExpectedVersion: params.ExpectedVersion,
		ReceiverInputID: params.InputID, Recovery: params.Recovery,
	}, params.RequestID)
}

func (s *Service) applicationContinuationCommit(ctx context.Context, params ApplicationContinuationCommitParams) (application.Continuation, error) {
	if strings.TrimSpace(params.RequestID) == "" || strings.TrimSpace(params.DeliveryID) == "" {
		return application.Continuation{}, errors.New("application continuation request_id and delivery_id are required")
	}
	scope, err := s.applicationInputScope(params.SessionID, params.ControllerToken, params.ExpectedGeneration)
	if err != nil {
		return application.Continuation{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.Continuation{}, errors.New("application state authority unavailable")
	}
	return s.artifacts.application.CommitContinuation(ctx, scope, application.ContinuationCommit{
		CompletionID: params.CompletionID, RunID: params.RunID, ReceiverInputID: params.DeliveryID,
	}, params.RequestID)
}
