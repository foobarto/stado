package broker

// EP-0064 application RPC. The WASM guest never sees this transport or its
// opaque binding token; fixed host imports select the operation and submit only
// the bounded operation payload. Broker admission and every call independently
// enforce the exact signed manifest capability.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker/application"
)

type applicationOperation struct {
	capability string
	call       func(context.Context, *application.Service, application.Authority, json.RawMessage, string) (any, error)
}

var applicationOperations = map[string]applicationOperation{
	"journal.append": {
		capability: "session:journal:append",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.JournalAppend, requestID string) (any, error) {
			return service.AppendJournal(ctx, auth, input, requestID)
		}),
	},
	"projection.read": {
		capability: "session:projection:read",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input projectionOptionsWire, _ string) (any, error) {
			return service.Project(ctx, auth, application.ProjectionOptions{
				JournalLimit: input.JournalLimit, ControlLimit: input.ControlLimit, CompletionLimit: input.CompletionLimit, WorkerLimit: input.WorkerLimit,
				DeferredTaskLimit: input.DeferredTaskLimit, DeferredTaskAfterOrdinal: input.DeferredTaskAfterOrdinal,
				IncludeTerminal: input.IncludeTerminal,
			})
		}),
	},
	"hold.acquire": {
		capability: "session:schedule",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input holdAcquireWire, requestID string) (any, error) {
			return service.AcquireHold(ctx, auth, application.HoldAcquire{
				ID: input.ID, RunID: input.RunID, ExpectedVersion: input.ExpectedVersion,
				ReasonCode: input.ReasonCode, Reason: input.Reason,
				TTL: time.Duration(input.TTLMS) * time.Millisecond,
			}, requestID)
		}),
	},
	"hold.release": {
		capability: "session:schedule",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.HoldCAS, requestID string) (any, error) {
			return service.ReleaseHold(ctx, auth, input, requestID)
		}),
	},
	"session.pause": {
		capability: "session:schedule",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.ControlInput, requestID string) (any, error) {
			return service.RequestPause(ctx, auth, input, requestID)
		}),
	},
	"session.stop": {
		capability: "session:schedule",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.ControlInput, requestID string) (any, error) {
			return service.RequestStop(ctx, auth, input, requestID)
		}),
	},
	"session.complete": {
		capability: "session:complete",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.CompletionInput, requestID string) (any, error) {
			return service.CompleteSession(ctx, auth, input, requestID)
		}),
	},
	"input.route": {
		capability: "session:input:route",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.OperatorInputRoute, requestID string) (any, error) {
			return service.RouteOperatorInput(ctx, auth, input, requestID)
		}),
	},
	"input.claim": {
		capability: "session:input:route",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.OperatorInputClaim, requestID string) (any, error) {
			return service.ClaimOperatorInput(ctx, auth, input, requestID)
		}),
	},
	"worker.request": {
		capability: "session:worker:request",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.WorkerRunRequest, requestID string) (any, error) {
			return service.RequestWorkerRun(ctx, auth, input, requestID)
		}),
	},
	"worker.resume": {
		capability: "session:worker:resume",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.WorkerRunCAS, requestID string) (any, error) {
			return service.RequestWorkerRunResume(ctx, auth, input, requestID)
		}),
	},
	"worker.cancel": {
		capability: "session:worker:cancel",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.WorkerRunCAS, requestID string) (any, error) {
			return service.CancelWorkerRun(ctx, auth, input, requestID)
		}),
	},
	"timer.schedule": {
		capability: "timer:schedule",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.TimerSchedule, requestID string) (any, error) {
			return service.ScheduleTimer(ctx, auth, input, requestID)
		}),
	},
	"timer.cancel": {
		capability: "timer:schedule",
		call: decodeApplicationCall(func(ctx context.Context, service *application.Service, auth application.Authority, input application.TimerCAS, requestID string) (any, error) {
			return service.CancelTimer(ctx, auth, input, requestID)
		}),
	},
}

type holdAcquireWire struct {
	ID              string `json:"id,omitempty"`
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	ReasonCode      string `json:"reason_code"`
	Reason          string `json:"reason,omitempty"`
	TTLMS           int64  `json:"ttl_ms"`
}

type projectionOptionsWire struct {
	JournalLimit             int    `json:"journal_limit,omitempty"`
	ControlLimit             int    `json:"control_limit,omitempty"`
	CompletionLimit          int    `json:"completion_limit,omitempty"`
	WorkerLimit              int    `json:"worker_limit,omitempty"`
	DeferredTaskLimit        int    `json:"deferred_task_limit,omitempty"`
	DeferredTaskAfterOrdinal uint64 `json:"deferred_task_after_ordinal,omitempty"`
	IncludeTerminal          bool   `json:"include_terminal,omitempty"`
}

func decodeApplicationCall[T any](call func(context.Context, *application.Service, application.Authority, T, string) (any, error)) func(context.Context, *application.Service, application.Authority, json.RawMessage, string) (any, error) {
	return func(ctx context.Context, service *application.Service, auth application.Authority, payload json.RawMessage, requestID string) (any, error) {
		var input T
		if err := strictUnmarshal(payload, &input); err != nil {
			return nil, err
		}
		return call(ctx, service, auth, input, requestID)
	}
}

func (s *Service) applicationCall(ctx context.Context, params ApplicationCallParams) (json.RawMessage, error) {
	if strings.TrimSpace(params.BindingToken) == "" || strings.TrimSpace(params.RequestID) == "" {
		return nil, errors.New("application binding_token and request_id are required")
	}
	if len(params.Payload) == 0 || len(params.Payload) > maxArtifactRPCBytes {
		return nil, fmt.Errorf("application payload size must be 1..%d bytes", maxArtifactRPCBytes)
	}
	operation, ok := applicationOperations[params.Operation]
	if !ok {
		return nil, fmt.Errorf("unknown application operation %q", params.Operation)
	}
	binding, err := s.applicationBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	if !binding.hasCapability(operation.capability) {
		return nil, fmt.Errorf("application capability %q is not admitted", operation.capability)
	}
	state := s.artifacts
	if state == nil || state.application == nil {
		return nil, errors.New("application state authority unavailable")
	}
	result, err := operation.call(ctx, state.application, binding.applicationAuthority(), params.Payload, params.RequestID)
	if err != nil {
		return nil, err
	}
	return boundedArtifactResponse(result)
}

func (s *Service) applicationEventsNext(ctx context.Context, params ApplicationEventsNextParams) ([]ApplicationEventResult, error) {
	if strings.TrimSpace(params.BindingToken) == "" {
		return nil, errors.New("application binding_token is required")
	}
	binding, err := s.applicationBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	state := s.artifacts
	if state == nil || state.application == nil {
		return nil, errors.New("application state authority unavailable")
	}
	if binding.hasCapability("timer:schedule") && containsApplicationEventKind(binding.eventKinds, "timer.due") {
		if err := state.application.PromoteDueTimers(ctx, binding.applicationAuthority(), params.Limit); err != nil {
			return nil, err
		}
	}
	if len(binding.eventKinds) == 0 {
		return []ApplicationEventResult{}, nil
	}
	events, _, err := state.application.PendingEvents(ctx, binding.applicationAuthority(), binding.eventKinds, params.Limit)
	if err != nil {
		return nil, err
	}
	result := make([]ApplicationEventResult, 0, len(events))
	for _, event := range events {
		result = append(result, ApplicationEventResult{
			Kind: event.Kind, WALSequence: event.WALSequence,
			EvidenceRefs: append([]string(nil), event.EvidenceRefs...),
			Data:         append(json.RawMessage(nil), event.Data...),
		})
	}
	return result, nil
}

func (s *Service) applicationEventsAck(ctx context.Context, params ApplicationEventsAckParams) (application.EventCursor, error) {
	if strings.TrimSpace(params.BindingToken) == "" || strings.TrimSpace(params.RequestID) == "" || params.Sequence == 0 {
		return application.EventCursor{}, errors.New("application binding_token, request_id, and sequence are required")
	}
	binding, err := s.applicationBinding(params.BindingToken)
	if err != nil {
		return application.EventCursor{}, err
	}
	state := s.artifacts
	if state == nil || state.application == nil {
		return application.EventCursor{}, errors.New("application state authority unavailable")
	}
	if len(binding.eventKinds) == 0 {
		return application.EventCursor{}, errors.New("application binding has no signed event subscriptions")
	}
	auth := binding.applicationAuthority()
	pending, cursor, err := state.application.PendingEvents(ctx, auth, binding.eventKinds, application.DefaultLimits().MaxProjectionItems)
	if err != nil {
		return application.EventCursor{}, err
	}
	// A retry of an already committed acknowledgement must reach the durable
	// idempotency check. A new cursor advance, however, must name an event that
	// this binding could receive under its signed manifest subscriptions.
	if params.Sequence > cursor.Sequence {
		allowed := false
		for _, event := range pending {
			if event.WALSequence == params.Sequence {
				allowed = true
				break
			}
		}
		// A mandatory-action event is intentionally absent from PendingEvents
		// once its route transition commits, so a crash between route and ack
		// cannot invoke the callback again. The cursor still needs to advance;
		// verify the settled event kind against the binding's signed subscription
		// before asking the application service to acknowledge it.
		if !allowed {
			kind, kindErr := state.application.EventKindAtSequence(ctx, auth, params.Sequence)
			if kindErr == nil {
				for _, subscribed := range binding.eventKinds {
					if kind == subscribed {
						allowed = true
						break
					}
				}
			}
		}
		if !allowed {
			return application.EventCursor{}, errors.New("application event sequence is not pending for signed subscriptions")
		}
	}
	return state.application.AcknowledgeEvent(ctx, auth, application.EventAck{Sequence: params.Sequence}, params.RequestID)
}

func (s *Service) applicationEventPublish(ctx context.Context, params ApplicationEventPublishParams) (ApplicationEventResult, error) {
	if strings.TrimSpace(params.SessionID) == "" || strings.TrimSpace(params.ControllerToken) == "" || strings.TrimSpace(params.RequestID) == "" || params.ExpectedGeneration == 0 {
		return ApplicationEventResult{}, errors.New("application event session_id, controller_token, expected_generation, and request_id are required")
	}
	state := s.artifacts
	if state == nil || state.application == nil {
		return ApplicationEventResult{}, errors.New("application state authority unavailable")
	}
	s.sessionsMu.RLock()
	session, ok := s.sessions[params.SessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return ApplicationEventResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return ApplicationEventResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, params.ControllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return ApplicationEventResult{}, err
	}
	generation := session.generation
	if params.ExpectedGeneration != generation {
		s.sessionsMu.RUnlock()
		return ApplicationEventResult{}, errors.New("application event session generation changed")
	}
	s.sessionsMu.RUnlock()
	event, err := state.application.PublishEvent(ctx, application.SessionScope{SessionID: params.SessionID, Generation: generation}, application.EventInput{
		ID: params.ID, Kind: params.Kind, Data: params.Data, EvidenceRefs: params.EvidenceRefs,
	}, params.RequestID)
	if err != nil {
		return ApplicationEventResult{}, err
	}
	return ApplicationEventResult{
		Kind: event.Kind, WALSequence: event.WALSequence,
		EvidenceRefs: append([]string(nil), event.EvidenceRefs...),
		Data:         append(json.RawMessage(nil), event.Data...),
	}, nil
}

func containsApplicationEventKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// SessionScheduleResult is a native enforcement projection. It is never
// available through a WASM host import because it aggregates isolated plugin
// namespaces. Pause/stop are requests for the shared loop to consume; holds
// are current broker facts and apply while their leases remain active.
type SessionScheduleResult = application.EnforcementProjection

func (s *Service) sessionSchedule(ctx context.Context, sessionID, controllerToken string) (SessionScheduleResult, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(controllerToken) == "" {
		return SessionScheduleResult{}, errors.New("session_id and controller_token required")
	}
	state := s.artifacts
	if state == nil || state.application == nil {
		return SessionScheduleResult{}, errors.New("application state authority unavailable")
	}
	s.sessionsMu.RLock()
	session, ok := s.sessions[sessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return SessionScheduleResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return SessionScheduleResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, controllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return SessionScheduleResult{}, err
	}
	generation := session.generation
	s.sessionsMu.RUnlock()
	return state.application.ProjectEnforcement(ctx, application.SessionScope{SessionID: sessionID, Generation: generation})
}

// sessionScheduleConsume durably closes an application-owned worker run before
// the native controller acts on a pause/stop result. It derives plugin
// authority from broker-owned projection state; the caller cannot name a run,
// plugin, outcome, reason, version, or idempotency key.
func (s *Service) sessionScheduleConsume(ctx context.Context, params SessionScheduleConsumeParams) (SessionScheduleResult, error) {
	if strings.TrimSpace(params.SessionID) == "" || strings.TrimSpace(params.ControllerToken) == "" || params.Sequence == 0 {
		return SessionScheduleResult{}, errors.New("session_id, controller_token, and sequence required")
	}
	state := s.artifacts
	if state == nil || state.application == nil {
		return SessionScheduleResult{}, errors.New("application state authority unavailable")
	}
	s.sessionsMu.RLock()
	session, ok := s.sessions[params.SessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return SessionScheduleResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return SessionScheduleResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, params.ControllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return SessionScheduleResult{}, err
	}
	generation, principal := session.generation, session.principal
	s.sessionsMu.RUnlock()

	scope := application.SessionScope{SessionID: params.SessionID, Generation: generation}
	projection, err := state.application.ProjectEnforcement(ctx, scope)
	if err != nil {
		return SessionScheduleResult{}, err
	}
	var status application.WorkerRunStatus
	var reason string
	switch {
	case projection.LatestStop != nil && projection.LatestStop.WALSequence == params.Sequence:
		status = application.WorkerRunStopped
		reason = projection.LatestStop.Reason
		if reason == "" {
			reason = projection.LatestStop.ReasonCode
		}
	case projection.LatestPause != nil && projection.LatestPause.WALSequence == params.Sequence &&
		(projection.LatestStop == nil || projection.LatestStop.WALSequence < params.Sequence) &&
		(projection.LatestCompletion == nil || projection.LatestCompletion.WALSequence < params.Sequence):
		status = application.WorkerRunInterrupted
		reason = projection.LatestPause.Reason
		if reason == "" {
			reason = projection.LatestPause.ReasonCode
		}
	default:
		return SessionScheduleResult{}, errors.New("schedule sequence is not the current consumable pause/stop result")
	}
	if projection.ActiveWorkerRun == nil {
		return projection, nil
	}
	run := projection.ActiveWorkerRun
	auth := application.Authority{
		SessionID: params.SessionID, Generation: generation, PluginID: run.PluginID,
		Principal: principal, Actor: "broker:scheduler",
	}
	_, err = state.application.TerminalizeWorkerRun(ctx, auth, application.WorkerRunTerminal{
		RunID: run.RunID, ExpectedVersion: run.Version, Status: status,
		Reason: reason, ControlSequence: params.Sequence,
	}, fmt.Sprintf("schedule-control:%d:%s:%s", params.Sequence, run.PluginID, run.RunID))
	if err != nil {
		return SessionScheduleResult{}, err
	}
	return state.application.ProjectEnforcement(ctx, scope)
}
