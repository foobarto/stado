package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/broker/wal"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

const (
	maxRetainedRPCBytes       = 128 << 10
	maxRetainedRequestIDBytes = 256
	maxRetainedTextBytes      = 64 << 10
	maxRetainedListItems      = 1000
)

var retainedRootBudget = brokerbudget.Limits{
	Tokens: 2_000_000, ToolCalls: 10_000, Turns: 2_000, WallSeconds: 86_400,
}

type retainedBrokerState struct {
	mu       sync.RWMutex
	opsMu    sync.Mutex
	registry *retained.Registry
	budgets  *brokerbudget.Ledger
	mailbox  *mailbox.Broker
	policy   *mailbox.DynamicRelationPolicy
	bindings map[string]retainedBinding
}

type retainedBinding struct {
	token             string
	sessionID         string
	generation        uint64
	controllerVersion uint64
	subject           string
	principal         string
	accountID         string
	parentSessionID   string
}

type RetainedAdmitRequest struct {
	ChildSessionID string                 `json:"child_session_id"`
	Purpose        string                 `json:"purpose"`
	Fork           retained.ForkPoint     `json:"fork_point"`
	CeilingDigest  string                 `json:"ceiling_digest"`
	Model          string                 `json:"model,omitempty"`
	ToolProfile    string                 `json:"tool_profile,omitempty"`
	Budget         brokerbudget.Limits    `json:"budget"`
	RestartPolicy  retained.RestartPolicy `json:"restart_policy,omitempty"`
}

type RetainedStartRequest struct {
	AdmissionID  string `json:"admission_id"`
	Generation   uint64 `json:"generation"`
	RuntimeNonce string `json:"runtime_nonce"`
	LeaseTTLMS   int64  `json:"lease_ttl_ms"`
}

type RetainedFinishRequest struct {
	AdmissionID   string                 `json:"admission_id"`
	Generation    uint64                 `json:"generation"`
	LeaseEpoch    uint64                 `json:"lease_epoch"`
	Usage         brokerbudget.Limits    `json:"usage"`
	Transient     bool                   `json:"transient,omitempty"`
	Error         string                 `json:"error,omitempty"`
	FinalText     string                 `json:"final_text,omitempty"`
	Cancelled     bool                   `json:"cancelled,omitempty"`
	RestartPolicy retained.RestartPolicy `json:"restart_policy,omitempty"`
}

type RetainedFinishResult struct {
	Admission retained.Admission `json:"admission"`
	Restart   bool               `json:"restart"`
	BackoffMS int64              `json:"backoff_ms,omitempty"`
}

type RetainedAdmissionRequest struct {
	AdmissionID string `json:"admission_id"`
	Generation  uint64 `json:"generation,omitempty"`
}

type RetainedGetResult struct {
	Admission retained.Admission `json:"admission"`
	Found     bool               `json:"found"`
}

type RetainedDeliverRequest struct {
	ReceiverSession string `json:"receiver_session"`
	SenderSession   string `json:"sender_session"`
}

type RetainedDeliverResult struct {
	Message mailbox.Message `json:"message"`
	Found   bool            `json:"found"`
}

type RetainedAckRequest struct {
	ReceiverSession    string `json:"receiver_session"`
	MessageID          string `json:"message_id"`
	DeliveryGeneration uint64 `json:"delivery_generation"`
	InputID            string `json:"input_id"`
}

type RetainedFollowUpRequest struct {
	AdmissionID string          `json:"admission_id"`
	Generation  uint64          `json:"generation"`
	Payload     json.RawMessage `json:"payload"`
}

func newRetainedBrokerState(store *wal.Store) (*retainedBrokerState, error) {
	if store == nil {
		return nil, errors.New("retained broker store required")
	}
	policy := mailbox.NewDynamicRelationPolicy()
	state := &retainedBrokerState{
		registry: retained.New(store), budgets: brokerbudget.New(store),
		mailbox: mailbox.New(store, policy), policy: policy,
		bindings: make(map[string]retainedBinding),
	}
	admissions, err := state.registry.List()
	if err != nil {
		return nil, err
	}
	for _, admission := range admissions {
		policy.Allow(admission.ParentSessionID, admission.ChildSessionID)
		policy.Allow(admission.ChildSessionID, admission.ParentSessionID)
	}
	return state, nil
}

func (s *Service) dispatchRetainedBind(raw json.RawMessage) (json.RawMessage, error) {
	var params RetainedBindParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodRetainedBind, err)
	}
	result, err := s.bindRetained(params)
	if err != nil {
		return nil, invalidArtifactParams(MethodRetainedBind, err)
	}
	return json.Marshal(result)
}

func (s *Service) dispatchRetainedCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var params RetainedCallParams
	if err := strictUnmarshal(raw, &params); err != nil {
		return nil, invalidArtifactParams(MethodRetainedCall, err)
	}
	result, err := s.retainedCall(ctx, params)
	if err != nil {
		return nil, invalidArtifactParams(MethodRetainedCall, err)
	}
	return json.Marshal(result)
}

func (s *Service) bindRetained(params RetainedBindParams) (RetainedBindResult, error) {
	state := s.retained
	if state == nil {
		return RetainedBindResult{}, errors.New("retained authority unavailable")
	}
	if strings.TrimSpace(params.SessionID) == "" || strings.TrimSpace(params.ControllerToken) == "" {
		return RetainedBindResult{}, errors.New("retained session_id and controller_token are required")
	}
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	session, ok := s.sessions[params.SessionID]
	if !ok {
		return RetainedBindResult{}, ErrSessionNotFound
	}
	if session.terminated {
		return RetainedBindResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, params.ControllerToken); err != nil {
		return RetainedBindResult{}, err
	}
	if !session.scope.durable || strings.TrimSpace(session.scope.subject) == "" {
		return RetainedBindResult{}, errors.New("retained execution requires a durable logical session")
	}
	if err := stadogit.ValidateSessionID(session.scope.subject); err != nil {
		return RetainedBindResult{}, fmt.Errorf("retained logical session: %w", err)
	}
	accountID := "session:" + session.scope.subject
	state.opsMu.Lock()
	defer state.opsMu.Unlock()
	account, found, err := state.budgets.GetAccount(accountID)
	if err != nil {
		return RetainedBindResult{}, err
	}
	if found {
		if account.ParentID != "" || account.RootID != accountID || account.Ceiling != retainedRootBudget {
			return RetainedBindResult{}, errors.New("retained root budget account conflicts with canonical limits")
		}
	} else if _, err := state.budgets.CreateAccount(context.Background(), accountID, "", retainedRootBudget, session.principal, "retained-broker", "retained-account:"+session.scope.subject); err != nil {
		return RetainedBindResult{}, err
	}
	tokenID, err := mintSessionID()
	if err != nil {
		return RetainedBindResult{}, fmt.Errorf("mint retained binding: %w", err)
	}
	binding := retainedBinding{
		token: "retained_" + tokenID, sessionID: params.SessionID,
		generation: session.generation, controllerVersion: session.controllerVersion,
		subject: session.scope.subject, principal: session.principal,
		accountID: accountID, parentSessionID: session.scope.subject,
	}
	state.mu.Lock()
	state.bindings[binding.token] = binding
	state.mu.Unlock()
	return RetainedBindResult{
		BindingToken: binding.token, AccountID: binding.accountID,
		Principal: binding.principal, ParentSessionID: binding.parentSessionID,
	}, nil
}

func (s *Service) retainedBinding(token string) (retainedBinding, error) {
	state := s.retained
	if state == nil {
		return retainedBinding{}, errors.New("retained authority unavailable")
	}
	state.mu.RLock()
	binding, ok := state.bindings[token]
	state.mu.RUnlock()
	if !ok || token == "" {
		return retainedBinding{}, errors.New("unknown retained binding")
	}
	s.sessionsMu.RLock()
	session, ok := s.sessions[binding.sessionID]
	active := ok && !session.terminated && session.generation == binding.generation &&
		session.controllerVersion == binding.controllerVersion && session.scope.durable &&
		session.scope.subject == binding.subject
	s.sessionsMu.RUnlock()
	if !active {
		return retainedBinding{}, errors.New("stale retained binding")
	}
	return binding, nil
}

func (s *Service) retainedCall(ctx context.Context, params RetainedCallParams) (any, error) {
	if strings.TrimSpace(params.BindingToken) == "" || strings.TrimSpace(params.RequestID) == "" {
		return nil, errors.New("retained binding_token and request_id are required")
	}
	if len(params.RequestID) > maxRetainedRequestIDBytes {
		return nil, errors.New("retained request_id is too large")
	}
	if len(params.Payload) == 0 || len(params.Payload) > maxRetainedRPCBytes {
		return nil, fmt.Errorf("retained payload size must be 1..%d bytes", maxRetainedRPCBytes)
	}
	binding, err := s.retainedBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	state := s.retained
	idem := retainedIdempotency(binding, params.Operation, params.RequestID)

	switch params.Operation {
	case "admit":
		var request RetainedAdmitRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.admit(ctx, binding, request, idem)
	case "start":
		var request RetainedStartRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.start(ctx, binding, request, idem)
	case "finish":
		var request RetainedFinishRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.finish(ctx, binding, request, idem)
	case "restart":
		var request RetainedAdmissionRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.restart(ctx, binding, request, idem)
	case "get":
		var request RetainedAdmissionRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		admission, found, err := state.ownedAdmission(binding, request.AdmissionID)
		return RetainedGetResult{Admission: admission, Found: found}, err
	case "list":
		var empty struct{}
		if err := strictUnmarshal(params.Payload, &empty); err != nil {
			return nil, err
		}
		return state.list(binding)
	case "deliver":
		var request RetainedDeliverRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.deliver(ctx, binding, request, idem)
	case "ack":
		var request RetainedAckRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.ack(ctx, binding, request, idem)
	case "followup":
		var request RetainedFollowUpRequest
		if err := strictUnmarshal(params.Payload, &request); err != nil {
			return nil, err
		}
		return state.followUp(ctx, binding, request, idem)
	default:
		return nil, fmt.Errorf("unknown retained operation %q", params.Operation)
	}
}

func (s *retainedBrokerState) admit(ctx context.Context, binding retainedBinding, request RetainedAdmitRequest, idem string) (retained.Admission, error) {
	if err := validateRetainedAdmit(request); err != nil {
		return retained.Admission{}, err
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	admissionID := retainedStableID("adm_", binding, idem)
	if existing, found, err := s.registry.Get(admissionID); err != nil {
		return retained.Admission{}, err
	} else if found {
		if existing.ParentSessionID != binding.parentSessionID || existing.ChildSessionID != request.ChildSessionID ||
			existing.Purpose != request.Purpose || existing.Fork != request.Fork || existing.CeilingDigest != request.CeilingDigest ||
			existing.Model != request.Model || existing.ToolProfile != request.ToolProfile {
			return retained.Admission{}, errors.New("retained request_id conflicts with existing admission")
		}
		return existing, nil
	}
	reservationID := retainedStableID("res_", binding, idem+":budget")
	reservation, err := s.budgets.ReserveNamed(ctx, reservationID, binding.accountID, request.Budget, binding.principal, "retained-broker", idem+":budget")
	if err != nil {
		return retained.Admission{}, err
	}
	admission, err := s.registry.Admit(ctx, retained.Request{
		AdmissionID: admissionID, ParentSessionID: binding.parentSessionID,
		ChildSessionID: request.ChildSessionID, Purpose: request.Purpose,
		Fork: request.Fork, CeilingDigest: request.CeilingDigest,
		Model: request.Model, ToolProfile: request.ToolProfile,
		BudgetReservationID: reservation.ID, Principal: binding.principal,
		Actor: "retained-broker", IdempotencyKey: idem + ":admission",
	})
	if err != nil {
		_, _ = s.budgets.Release(context.Background(), reservation.ID, binding.principal, "retained-broker", idem+":release")
		return retained.Admission{}, err
	}
	s.policy.Allow(admission.ParentSessionID, admission.ChildSessionID)
	s.policy.Allow(admission.ChildSessionID, admission.ParentSessionID)
	return admission, nil
}

func (s *retainedBrokerState) start(ctx context.Context, binding retainedBinding, request RetainedStartRequest, idem string) (retained.Admission, error) {
	if request.Generation == 0 || request.RuntimeNonce == "" || request.LeaseTTLMS < 1000 || request.LeaseTTLMS > int64((5*time.Minute)/time.Millisecond) {
		return retained.Admission{}, errors.New("invalid retained start fence or lease TTL")
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	admission, found, err := s.ownedAdmission(binding, request.AdmissionID)
	if err != nil || !found {
		return retained.Admission{}, firstRetainedError(err, retained.ErrNotFound)
	}
	if admission.Generation != request.Generation || admission.RuntimeNonce != request.RuntimeNonce {
		return retained.Admission{}, retained.ErrLease
	}
	if admission.Status == retained.StatusRunning {
		return admission, nil
	}
	if admission.Status == retained.StatusAdmitted {
		if admission.LeaseEpoch == 0 || !admission.LeaseUntil.After(time.Now()) {
			admission, err = s.registry.AcquireLease(ctx, admission.ID, admission.RuntimeNonce, binding.principal, "retained-runtime", idem+":lease", time.Duration(request.LeaseTTLMS)*time.Millisecond)
			if err != nil {
				return retained.Admission{}, err
			}
		}
		admission, err = s.registry.Transition(ctx, admission.ID, retained.StatusAdmitted, retained.StatusStarting, admission.LeaseEpoch, binding.principal, "retained-runtime", idem+":starting")
		if err != nil {
			return retained.Admission{}, err
		}
	}
	if admission.Status != retained.StatusStarting {
		return retained.Admission{}, retained.ErrLifecycle
	}
	return s.registry.Transition(ctx, admission.ID, retained.StatusStarting, retained.StatusRunning, admission.LeaseEpoch, binding.principal, "retained-runtime", idem+":running")
}

func (s *retainedBrokerState) finish(ctx context.Context, binding retainedBinding, request RetainedFinishRequest, idem string) (RetainedFinishResult, error) {
	if request.Generation == 0 || request.LeaseEpoch == 0 || len(request.Error) > maxRetainedTextBytes || len(request.FinalText) > maxRetainedTextBytes {
		return RetainedFinishResult{}, errors.New("invalid retained finish fence or bounded text")
	}
	if err := validateRestartPolicy(request.RestartPolicy); err != nil {
		return RetainedFinishResult{}, err
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	admission, found, err := s.ownedAdmission(binding, request.AdmissionID)
	if err != nil || !found {
		return RetainedFinishResult{}, firstRetainedError(err, retained.ErrNotFound)
	}
	if admission.Generation != request.Generation || admission.LeaseEpoch != request.LeaseEpoch {
		return RetainedFinishResult{}, retained.ErrLease
	}
	if admission.Status == retained.StatusDown {
		return RetainedFinishResult{Admission: admission, Restart: true}, nil
	}
	if admission.Status == retained.StatusCompleted || admission.Status == retained.StatusFailed || admission.Status == retained.StatusCancelled {
		if err := s.sendFinalReply(ctx, binding, admission, request.FinalText, idem); err != nil {
			return RetainedFinishResult{}, err
		}
		return RetainedFinishResult{Admission: admission}, nil
	}
	if admission.Status != retained.StatusRunning {
		return RetainedFinishResult{}, retained.ErrLifecycle
	}
	status := retained.StatusCompleted
	failureClass := retained.FailureLogical
	if request.Cancelled {
		status, failureClass = retained.StatusCancelled, retained.FailureCancelled
	} else if request.Error != "" {
		status = retained.StatusFailed
		if request.Transient {
			failureClass = retained.FailureTransient
		}
	}
	if _, commitErr := s.budgets.Commit(ctx, admission.BudgetReservationID, request.Usage, binding.principal, "provider-usage", idem+":usage"); commitErr != nil {
		status, failureClass = retained.StatusFailed, retained.FailureBudget
		request.Transient = false
	}
	admission, err = s.registry.Transition(ctx, admission.ID, retained.StatusRunning, status, admission.LeaseEpoch, binding.principal, "retained-runtime", idem+":terminal")
	if err != nil {
		return RetainedFinishResult{}, err
	}
	result := RetainedFinishResult{Admission: admission}
	if status == retained.StatusFailed && request.Transient {
		decision, decisionErr := s.registry.DecideRestart(admission.ID, failureClass, request.RestartPolicy, binding.principal, "retained-supervisor", idem+":restart-decision")
		if decisionErr != nil {
			return RetainedFinishResult{}, decisionErr
		}
		if decision.Restart {
			admission, err = s.registry.Transition(ctx, admission.ID, retained.StatusFailed, retained.StatusDown, admission.LeaseEpoch, binding.principal, "retained-supervisor", idem+":down")
			if err != nil {
				return RetainedFinishResult{}, err
			}
			return RetainedFinishResult{Admission: admission, Restart: true, BackoffMS: decision.Backoff.Milliseconds()}, nil
		}
	}
	if err := s.sendFinalReply(ctx, binding, admission, request.FinalText, idem); err != nil {
		return RetainedFinishResult{}, err
	}
	return result, nil
}

func (s *retainedBrokerState) sendFinalReply(ctx context.Context, binding retainedBinding, admission retained.Admission, finalText, idem string) error {
	if finalText == "" {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"text": finalText})
	_, err := s.mailbox.Send(ctx, mailbox.SendRequest{
		MessageID:     retainedStableID("msg_", binding, idem+":reply"),
		SenderSession: admission.ChildSessionID, SenderGeneration: admission.Generation,
		ReceiverSession: admission.ParentSessionID, Kind: mailbox.KindReply,
		CorrelationID: admission.ID, Payload: payload, Principal: binding.principal,
		Actor: "retained-runtime", IdempotencyKey: idem + ":reply",
	})
	return err
}

func (s *retainedBrokerState) restart(ctx context.Context, binding retainedBinding, request RetainedAdmissionRequest, idem string) (retained.Admission, error) {
	if request.Generation == 0 {
		return retained.Admission{}, errors.New("retained restart generation is required")
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	admission, found, err := s.ownedAdmission(binding, request.AdmissionID)
	if err != nil || !found {
		return retained.Admission{}, firstRetainedError(err, retained.ErrNotFound)
	}
	if admission.Generation == request.Generation+1 && admission.Status == retained.StatusAdmitted {
		return admission, nil
	}
	if admission.Generation != request.Generation || admission.Status != retained.StatusDown {
		return retained.Admission{}, retained.ErrLifecycle
	}
	return s.registry.RestartGeneration(ctx, admission.ID, binding.principal, "retained-supervisor", idem+":generation")
}

func (s *retainedBrokerState) deliver(ctx context.Context, binding retainedBinding, request RetainedDeliverRequest, idem string) (RetainedDeliverResult, error) {
	if request.ReceiverSession == "" || request.SenderSession == "" {
		return RetainedDeliverResult{}, errors.New("retained delivery endpoints are required")
	}
	if _, found, err := s.ownedRelationship(binding, request.ReceiverSession, request.SenderSession); err != nil || !found {
		return RetainedDeliverResult{}, firstRetainedError(err, mailbox.ErrUnauthorized)
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	message, found, err := s.mailbox.DeliverFrom(ctx, request.ReceiverSession, request.SenderSession, binding.principal, "retained-broker", idem)
	return RetainedDeliverResult{Message: message, Found: found}, err
}

func (s *retainedBrokerState) ack(ctx context.Context, binding retainedBinding, request RetainedAckRequest, idem string) (mailbox.Message, error) {
	if request.ReceiverSession == "" || request.MessageID == "" || request.DeliveryGeneration == 0 || request.InputID == "" {
		return mailbox.Message{}, errors.New("retained acknowledgement fields are required")
	}
	if request.ReceiverSession != binding.parentSessionID {
		owned := false
		admissions, err := s.registry.List()
		if err != nil {
			return mailbox.Message{}, err
		}
		for _, admission := range admissions {
			if admission.ParentSessionID == binding.parentSessionID && admission.ChildSessionID == request.ReceiverSession {
				owned = true
				break
			}
		}
		if !owned {
			return mailbox.Message{}, mailbox.ErrUnauthorized
		}
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	return s.mailbox.CommitReceiverInput(ctx, request.ReceiverSession, request.MessageID, request.DeliveryGeneration, request.InputID, binding.principal, "retained-broker", idem)
}

func (s *retainedBrokerState) followUp(ctx context.Context, binding retainedBinding, request RetainedFollowUpRequest, idem string) (mailbox.Message, error) {
	if request.Generation == 0 || len(request.Payload) == 0 || len(request.Payload) > maxRetainedTextBytes || !json.Valid(request.Payload) {
		return mailbox.Message{}, errors.New("invalid retained follow-up")
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	admission, found, err := s.ownedAdmission(binding, request.AdmissionID)
	if err != nil || !found {
		return mailbox.Message{}, firstRetainedError(err, retained.ErrNotFound)
	}
	if admission.Generation != request.Generation {
		return mailbox.Message{}, retained.ErrLease
	}
	if admission.Status != retained.StatusAdmitted && admission.Status != retained.StatusStarting && admission.Status != retained.StatusRunning {
		return mailbox.Message{}, retained.ErrLifecycle
	}
	return s.mailbox.Send(ctx, mailbox.SendRequest{
		MessageID:     retainedStableID("msg_", binding, idem+":followup"),
		SenderSession: binding.parentSessionID, SenderGeneration: binding.generation,
		ReceiverSession: admission.ChildSessionID, Kind: mailbox.KindRequest,
		Payload: append(json.RawMessage(nil), request.Payload...), Principal: binding.principal,
		Actor: "retained-broker", IdempotencyKey: idem + ":followup",
	})
}

func (s *retainedBrokerState) ownedAdmission(binding retainedBinding, id string) (retained.Admission, bool, error) {
	if strings.TrimSpace(id) == "" {
		return retained.Admission{}, false, errors.New("retained admission_id is required")
	}
	admission, found, err := s.registry.Get(id)
	if err != nil || !found || admission.ParentSessionID != binding.parentSessionID {
		return retained.Admission{}, false, err
	}
	return admission, true, nil
}

func (s *retainedBrokerState) ownedRelationship(binding retainedBinding, receiver, sender string) (retained.Admission, bool, error) {
	admissions, err := s.registry.List()
	if err != nil {
		return retained.Admission{}, false, err
	}
	for _, admission := range admissions {
		if admission.ParentSessionID != binding.parentSessionID {
			continue
		}
		if (receiver == admission.ParentSessionID && sender == admission.ChildSessionID) ||
			(receiver == admission.ChildSessionID && sender == admission.ParentSessionID) {
			return admission, true, nil
		}
	}
	return retained.Admission{}, false, nil
}

func (s *retainedBrokerState) list(binding retainedBinding) ([]retained.Admission, error) {
	admissions, err := s.registry.List()
	if err != nil {
		return nil, err
	}
	out := make([]retained.Admission, 0, len(admissions))
	for _, admission := range admissions {
		if admission.ParentSessionID == binding.parentSessionID {
			out = append(out, admission)
			if len(out) > maxRetainedListItems {
				return nil, errors.New("retained child projection exceeds bounded list limit")
			}
		}
	}
	return out, nil
}

func validateRetainedAdmit(request RetainedAdmitRequest) error {
	parsed, err := uuid.Parse(request.ChildSessionID)
	if err != nil || parsed.String() != request.ChildSessionID || stadogit.ValidateSessionID(request.ChildSessionID) != nil {
		return errors.New("retained child_session_id must be a canonical UUID")
	}
	if strings.TrimSpace(request.Purpose) == "" || len(request.Purpose) > 128 || len(request.Model) > 256 || len(request.ToolProfile) > 128 {
		return errors.New("retained purpose or runtime selector is invalid")
	}
	if request.Fork.SourceGeneration == 0 || request.Fork.CommittedTurn < 0 {
		return errors.New("retained fork generation and turn are required")
	}
	if err := retained.ValidateForkPoint(request.Fork); err != nil {
		return err
	}
	if len(request.CeilingDigest) != sha256.Size*2 {
		return errors.New("retained ceiling_digest must be lowercase SHA-256 hex")
	}
	decoded, err := hex.DecodeString(request.CeilingDigest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(request.CeilingDigest) != request.CeilingDigest {
		return errors.New("retained ceiling_digest must be lowercase SHA-256 hex")
	}
	if request.Budget.Tokens == 0 || request.Budget.Turns == 0 || request.Budget.WallSeconds == 0 {
		return errors.New("retained token, turn, and wall-time budgets are required")
	}
	return validateRestartPolicy(request.RestartPolicy)
}

func validateRestartPolicy(policy retained.RestartPolicy) error {
	if policy == (retained.RestartPolicy{}) {
		return nil
	}
	if policy.Mode != "on_transient_failure" || policy.MaxRestarts < 1 || policy.MaxRestarts > 5 ||
		policy.Window != 10*time.Minute || policy.BaseBackoff != 250*time.Millisecond || policy.MaxBackoff != 10*time.Second {
		return errors.New("retained restart policy exceeds the fixed supervision envelope")
	}
	return nil
}

func retainedIdempotency(binding retainedBinding, operation, requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("retained:%s:g%d:%s:%x", binding.subject, binding.generation, operation, digest[:])
}

func retainedStableID(prefix string, binding retainedBinding, seed string) string {
	digest := sha256.Sum256([]byte(binding.subject + "\x00" + seed))
	return prefix + hex.EncodeToString(digest[:])
}

func firstRetainedError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
