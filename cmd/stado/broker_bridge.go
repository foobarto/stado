package main

// broker_bridge.go — adapter glue between internal/broker.Service and
// internal/daemon.BrokerDispatcher.
//
// internal/broker.Service.Dispatch returns (json.RawMessage, error)
// with *broker.DispatchError carrying the JSON-RPC code+message on
// failure. internal/daemon.BrokerDispatcher expects a *daemon.Error
// (the existing protocol envelope) on failure. The bridge translates
// between the two.
//
// The bridge lives in cmd/stado because it's the only package that
// imports both internal/broker and internal/daemon: internal/daemon
// deliberately does not depend on internal/broker (avoids upward
// dependency from the protocol layer to the policy layer).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
)

// brokerPolicyFilename is the per-operator override file the broker
// reads from $XDG_CONFIG_HOME/stado/. Absence is fine — the
// embedded permissive default is used instead.
const brokerPolicyFilename = "policy.toml"

// buildBrokerService loads the operator's policy file (or the
// embedded default if absent), constructs a broker.Service, and
// returns it ready to wire into ServerOpts.BrokerDispatcher via
// brokerDispatcherBridge.
//
// Phase 1: no decision log writer is configured here (decisionsLog
// is nil); phase 5 wires the canonical $XDG_DATA_HOME/stado/broker/
// decisions.jsonl path.
func buildBrokerService() (*broker.Service, error) {
	policyPath := filepath.Join(config.ConfigDir(), brokerPolicyFilename)
	policy, err := broker.LoadOrDefault(policyPath)
	if err != nil {
		return nil, err
	}
	return broker.NewService(policy, nil), nil
}

// brokerDispatcherBridge wraps svc.Dispatch in the shape
// daemon.ServerOpts.BrokerDispatcher expects. The returned closure
// preserves the broker's DispatchError code/message when present;
// other errors are surfaced as ErrCodeBrokerInternal with the
// error text in the message.
func brokerDispatcherBridge(svc *broker.Service) daemon.BrokerDispatcher {
	return func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *daemon.Error) {
		result, err := svc.Dispatch(ctx, method, params)
		if err == nil {
			return result, nil
		}
		var de *broker.DispatchError
		if errors.As(err, &de) {
			return nil, &daemon.Error{
				Code:    de.Code,
				Message: de.Message,
				Data:    de.Data,
			}
		}
		return nil, &daemon.Error{
			Code:    daemon.ErrCodeBrokerInternal,
			Message: "broker: " + err.Error(),
		}
	}
}
