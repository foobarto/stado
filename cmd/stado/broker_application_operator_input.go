package main

import (
	"context"
	"errors"
	"strconv"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/runtime"
)

func (s *BrokerSession) applicationInputGeneration() (uint64, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" {
		return 0, errors.New("application operator-input broker unavailable for this session")
	}
	s.applicationMu.RLock()
	defer s.applicationMu.RUnlock()
	if s.applicationGeneration == 0 {
		return 0, errors.New("application operator-input broker has no admitted session generation")
	}
	return s.applicationGeneration, nil
}

func (s *BrokerSession) CaptureApplicationOperatorInput(ctx context.Context, text, requestID string) (runtime.ApplicationOperatorInput, error) {
	generation, err := s.applicationInputGeneration()
	if err != nil {
		return runtime.ApplicationOperatorInput{}, err
	}
	var result application.OperatorInput
	err = s.client.Call(ctx, broker.MethodApplicationInputCapture, broker.ApplicationInputCaptureParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken, ExpectedGeneration: generation,
		RequestID: requestID, Text: text,
	}, &result)
	return runtimeOperatorInput(result), err
}

func (s *BrokerSession) ApplicationOperatorInputState(ctx context.Context) (runtime.ApplicationInputState, error) {
	generation, err := s.applicationInputGeneration()
	if err != nil {
		return runtime.ApplicationInputState{}, err
	}
	var result broker.ApplicationInputStateResult
	if err := s.client.Call(ctx, broker.MethodApplicationInputState, broker.ApplicationInputStateParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken, ExpectedGeneration: generation,
	}, &result); err != nil {
		return runtime.ApplicationInputState{}, err
	}
	out := runtime.ApplicationInputState{
		AsOfSequence: result.AsOfSequence, OpenDeferredTaskCount: result.OpenDeferredTaskCount,
		OpenDeferredTruncated: result.OpenDeferredTruncated,
	}
	if result.ActiveWorkerRun != nil {
		run := runtimeApplicationWorkerRun(*result.ActiveWorkerRun)
		out.ActiveWorkerRun = &run
	}
	for _, input := range result.ReadyInputs {
		out.ReadyInputs = append(out.ReadyInputs, runtimeOperatorInput(input))
	}
	for _, input := range result.ReviewingInputs {
		out.ReviewingInputs = append(out.ReviewingInputs, runtimeOperatorInput(input))
	}
	for _, input := range result.RecoveryInputs {
		out.RecoveryInputs = append(out.RecoveryInputs, runtimeOperatorInput(input))
	}
	for _, task := range result.OpenDeferredTasks {
		out.OpenDeferredTasks = append(out.OpenDeferredTasks, runtime.ApplicationDeferredTask{
			ID: task.ID, InputID: task.InputID, RunID: task.RunID, Ordinal: task.Ordinal,
			Title: task.Title, Status: task.Status,
		})
	}
	if result.PendingContinuation != nil {
		continuation := runtimeApplicationContinuation(*result.PendingContinuation, result.PendingContinuationInputs)
		out.PendingContinuation = &continuation
	}
	return out, nil
}

func (s *BrokerSession) CommitApplicationOperatorInput(ctx context.Context, input runtime.ApplicationOperatorInput, recovery bool) (runtime.ApplicationOperatorInput, error) {
	generation, err := s.applicationInputGeneration()
	if err != nil {
		return runtime.ApplicationOperatorInput{}, err
	}
	var result application.OperatorInput
	err = s.client.Call(ctx, broker.MethodApplicationInputCommit, broker.ApplicationInputCommitParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken, ExpectedGeneration: generation,
		RequestID: "receiver-input:" + input.ID + ":" + strconv.FormatUint(input.Version, 10),
		InputID:   input.ID, ExpectedVersion: input.Version, Recovery: recovery,
	}, &result)
	return runtimeOperatorInput(result), err
}

func (s *BrokerSession) CommitApplicationContinuation(ctx context.Context, input runtime.ApplicationContinuation) (runtime.ApplicationContinuation, error) {
	generation, err := s.applicationInputGeneration()
	if err != nil {
		return runtime.ApplicationContinuation{}, err
	}
	var result application.Continuation
	err = s.client.Call(ctx, broker.MethodApplicationContinuationCommit, broker.ApplicationContinuationCommitParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken, ExpectedGeneration: generation,
		RequestID:    "receiver-continuation:" + input.DeliveryID,
		CompletionID: input.CompletionID, RunID: input.RunID, DeliveryID: input.DeliveryID,
	}, &result)
	return runtimeApplicationContinuation(result, nil), err
}

func runtimeOperatorInput(input application.OperatorInput) runtime.ApplicationOperatorInput {
	return runtime.ApplicationOperatorInput{
		ID: input.ID, SessionID: input.SessionID, Generation: input.Generation,
		PluginID: input.PluginID, RunID: input.RunID, Ordinal: input.Ordinal,
		Version: input.Version, WALSequence: input.WALSequence, Text: input.Text,
		Digest: input.Digest, Status: string(input.Status), ReviewID: input.ReviewID, Recovered: input.Recovered,
	}
}

func runtimeApplicationContinuation(input application.Continuation, inputs []application.ContinuationInput) runtime.ApplicationContinuation {
	out := runtime.ApplicationContinuation{
		CompletionID: input.CompletionID, DeliveryID: input.DeliveryID,
		SessionID: input.SessionID, Generation: input.Generation, PluginID: input.PluginID,
		RunID: input.RunID, InputIDs: append([]string(nil), input.InputIDs...),
		Status: string(input.Status), WALSequence: input.WALSequence,
	}
	for _, item := range inputs {
		out.Inputs = append(out.Inputs, runtime.ApplicationContinuationInput{ID: item.ID, Ordinal: item.Ordinal, Text: item.Text, Digest: item.Digest})
	}
	return out
}

func runtimeApplicationWorkerRun(run application.WorkerRun) runtime.ApplicationWorkerRun {
	return runtime.ApplicationWorkerRun{
		SessionID: run.SessionID, Generation: run.Generation, PluginID: run.PluginID,
		RunID: run.RunID, Version: run.Version, WALSequence: run.WALSequence,
		Objective: run.Objective, Prompt: run.Prompt,
		Conflict: runtime.ApplicationWorkerRunConflict(run.Conflict), Status: runtime.ApplicationWorkerRunStatus(run.Status),
		TerminalReason: run.TerminalReason, TerminalSequence: run.TerminalSequence,
	}
}

var _ runtime.ApplicationOperatorInputController = (*BrokerSession)(nil)
