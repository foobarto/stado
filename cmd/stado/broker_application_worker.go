package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/daemon"
)

// brokerApplicationControllerBridge is held only by the native lifecycle
// surface. Every operation crosses a controller-authenticated broker method;
// the ordinary application binding remains only a selector for the exact
// admitted plugin namespace and cannot authorize these transitions alone.
type brokerApplicationControllerBridge struct {
	client          *daemon.Client
	sessionID       string
	controllerToken string
	bindingToken    string
}

type applicationWorkerGetWire struct {
	RunID string `json:"run_id"`
}

type applicationWorkerTransitionWire struct {
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

func (b *brokerApplicationControllerBridge) CallApplicationController(ctx context.Context, operation string, payload []byte) ([]byte, error) {
	if b == nil || b.client == nil || b.sessionID == "" || b.controllerToken == "" || b.bindingToken == "" {
		return nil, errors.New("application controller bridge unavailable")
	}
	var result application.WorkerRun
	switch operation {
	case "worker.get":
		var input applicationWorkerGetWire
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, fmt.Errorf("decode worker get: %w", err)
		}
		if err := b.client.Call(ctx, broker.MethodApplicationWorkerGet, broker.ApplicationWorkerGetParams{
			SessionID: b.sessionID, ControllerToken: b.controllerToken,
			BindingToken: b.bindingToken, RunID: input.RunID,
		}, &result); err != nil {
			return nil, err
		}
	case "worker.activate", "worker.resume.activate", "worker.cancel":
		var input applicationWorkerTransitionWire
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, fmt.Errorf("decode worker transition: %w", err)
		}
		method := broker.MethodApplicationWorkerActivate
		if operation == "worker.resume.activate" {
			method = broker.MethodApplicationWorkerResumeActivate
		} else if operation == "worker.cancel" {
			method = broker.MethodApplicationWorkerCancel
		}
		if err := b.client.Call(ctx, method, broker.ApplicationWorkerTransitionParams{
			SessionID: b.sessionID, ControllerToken: b.controllerToken,
			BindingToken: b.bindingToken, RunID: input.RunID,
			ExpectedVersion: input.ExpectedVersion, Reason: input.Reason,
		}, &result); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown application controller operation %q", operation)
	}
	return json.Marshal(result)
}
