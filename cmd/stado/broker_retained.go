package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/orchestration"
	"github.com/foobarto/stado/internal/runtime"
)

var _ runtime.RetainedBackendProvider = (*BrokerSession)(nil)
var _ orchestration.RetainedBackend = (*brokerRetainedBackend)(nil)

type brokerRetainedBackend struct {
	client *daemon.Client
	token  string
}

func (s *BrokerSession) BindRetainedBackend(ctx context.Context) (runtime.RetainedBackendBinding, error) {
	client, sessionID, controllerToken, ok := s.snapshotControllerAuthority()
	if !ok {
		return runtime.RetainedBackendBinding{}, errors.New("retained broker unavailable for this session")
	}
	var result broker.RetainedBindResult
	if err := client.Call(ctx, broker.MethodRetainedBind, broker.RetainedBindParams{
		SessionID: sessionID, ControllerToken: controllerToken,
	}, &result); err != nil {
		return runtime.RetainedBackendBinding{}, err
	}
	if result.BindingToken == "" || result.AccountID == "" || result.Principal == "" || result.ParentSessionID == "" {
		return runtime.RetainedBackendBinding{}, errors.New("broker returned an incomplete retained binding")
	}
	return runtime.RetainedBackendBinding{
		Backend:   &brokerRetainedBackend{client: client, token: result.BindingToken},
		AccountID: result.AccountID, Principal: result.Principal,
		ParentSessionID: result.ParentSessionID,
	}, nil
}

func (b *brokerRetainedBackend) call(ctx context.Context, operation, requestID string, payload, result any) error {
	if b == nil || b.client == nil || b.token == "" {
		return errors.New("retained broker binding unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return b.client.Call(ctx, broker.MethodRetainedCall, broker.RetainedCallParams{
		BindingToken: b.token, RequestID: requestID, Operation: operation, Payload: raw,
	}, result)
}

func (b *brokerRetainedBackend) AdmitRetained(ctx context.Context, request orchestration.LaunchRequest) (retained.Admission, error) {
	var result retained.Admission
	err := b.call(ctx, "admit", request.IdempotencyKey, broker.RetainedAdmitRequest{
		ChildSessionID: request.Admission.ChildSessionID, Purpose: request.Admission.Purpose,
		Fork: request.Admission.Fork, CeilingDigest: request.Admission.CeilingDigest,
		Model: request.Admission.Model, ToolProfile: request.Admission.ToolProfile,
		Budget: request.Budget, RestartPolicy: request.RestartPolicy,
	}, &result)
	return result, err
}

func (b *brokerRetainedBackend) StartRetained(ctx context.Context, admission retained.Admission, request orchestration.LaunchRequest, ttl time.Duration) (retained.Admission, error) {
	var result retained.Admission
	err := b.call(ctx, "start", fmt.Sprintf("%s:start:g%d", request.IdempotencyKey, admission.Generation), broker.RetainedStartRequest{
		AdmissionID: admission.ID, Generation: admission.Generation,
		RuntimeNonce: admission.RuntimeNonce, LeaseTTLMS: ttl.Milliseconds(),
	}, &result)
	return result, err
}

func (b *brokerRetainedBackend) FinishRetained(ctx context.Context, admission retained.Admission, request orchestration.LaunchRequest, launch orchestration.LaunchResult, cancelled bool) (orchestration.RetainedFinish, error) {
	var result broker.RetainedFinishResult
	err := b.call(ctx, "finish", fmt.Sprintf("%s:finish:g%d", request.IdempotencyKey, admission.Generation), broker.RetainedFinishRequest{
		AdmissionID: admission.ID, Generation: admission.Generation, LeaseEpoch: admission.LeaseEpoch,
		Usage: launch.Usage, Transient: launch.Transient, Error: launch.Error,
		FinalText: launch.FinalText, Cancelled: cancelled, RestartPolicy: request.RestartPolicy,
	}, &result)
	return orchestration.RetainedFinish{
		Admission: result.Admission, Restart: result.Restart,
		Backoff: time.Duration(result.BackoffMS) * time.Millisecond,
	}, err
}

func (b *brokerRetainedBackend) RestartRetained(ctx context.Context, admission retained.Admission, request orchestration.LaunchRequest) (retained.Admission, error) {
	var result retained.Admission
	err := b.call(ctx, "restart", fmt.Sprintf("%s:restart:g%d", request.IdempotencyKey, admission.Generation), broker.RetainedAdmissionRequest{
		AdmissionID: admission.ID, Generation: admission.Generation,
	}, &result)
	return result, err
}

func (b *brokerRetainedBackend) GetRetained(id string) (retained.Admission, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
	defer cancel()
	var result broker.RetainedGetResult
	err := b.call(ctx, "get", "get:"+id, broker.RetainedAdmissionRequest{AdmissionID: id}, &result)
	return result.Admission, result.Found, err
}

func (b *brokerRetainedBackend) ListRetained() ([]retained.Admission, error) {
	ctx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
	defer cancel()
	var result []retained.Admission
	err := b.call(ctx, "list", "list", struct{}{}, &result)
	return result, err
}

func (b *brokerRetainedBackend) DeliverRetained(ctx context.Context, receiver, sender, _, _, idem string) (mailbox.Message, bool, error) {
	var result broker.RetainedDeliverResult
	err := b.call(ctx, "deliver", idem, broker.RetainedDeliverRequest{
		ReceiverSession: receiver, SenderSession: sender,
	}, &result)
	return result.Message, result.Found, err
}

func (b *brokerRetainedBackend) CommitRetainedInput(ctx context.Context, receiver, messageID string, generation uint64, inputID, _, _, idem string) (mailbox.Message, error) {
	var result mailbox.Message
	err := b.call(ctx, "ack", idem, broker.RetainedAckRequest{
		ReceiverSession: receiver, MessageID: messageID,
		DeliveryGeneration: generation, InputID: inputID,
	}, &result)
	return result, err
}

func (b *brokerRetainedBackend) FollowUp(ctx context.Context, _ string, handle orchestration.Handle, payload []byte, _, _, idem string) (mailbox.Message, error) {
	var result mailbox.Message
	err := b.call(ctx, "followup", idem, broker.RetainedFollowUpRequest{
		AdmissionID: handle.AdmissionID, Generation: handle.Generation,
		Payload: append(json.RawMessage(nil), payload...),
	}, &result)
	return result, err
}
