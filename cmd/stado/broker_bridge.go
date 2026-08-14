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
	"fmt"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
)

// brokerPolicyFilename is the per-operator override file the broker
// reads from $XDG_CONFIG_HOME/stado/. Absence is fine — the
// embedded permissive default is used instead.
const brokerPolicyFilename = "policy.toml"

// brokerDecisionLogPath returns the canonical path the broker
// appends decision records to: $XDG_DATA_HOME/stado/broker/
// decisions.jsonl (mirrors the path the mount table reserves as
// ModeBrokerOnly).
func brokerDecisionLogPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir(), "broker", "decisions.jsonl")
}

// buildBrokerService loads the operator's policy file (or the
// embedded default if absent), opens the broker-decision log
// file for append, constructs a broker.Service wired to both,
// and returns it ready to wire into ServerOpts.BrokerDispatcher
// via brokerDispatcherBridge.
//
// Phase 5: the decision log is the canonical record of broker
// admit/deny actions and is what operators inspect for forensic
// walk-back. See DESIGN.md §"Audit" → "Broker-decision log".
func buildBrokerService(cfg *config.Config) (*broker.Service, error) {
	policyPath := filepath.Join(config.ConfigDir(), brokerPolicyFilename)
	policy, err := broker.LoadOrDefault(policyPath)
	if err != nil {
		return nil, err
	}

	writer, err := openBrokerDecisionLog(cfg)
	if err != nil {
		return nil, fmt.Errorf("open broker-decision log: %w", err)
	}
	svc := broker.NewService(policy, writer)
	store, err := wal.OpenShared(filepath.Join(cfg.StateDir(), "broker", "events"))
	if err != nil {
		return nil, fmt.Errorf("open broker artifact WAL: %w", err)
	}
	if err := svc.ConfigureArtifactStore(store, brokerInstalledPluginVerifier{cfg: cfg}); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("configure broker artifact authority: %w", err)
	}
	if err := svc.ConfigureSessionLineageVerifier(newBrokerSessionLineageVerifier(cfg)); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("configure broker session lineage authority: %w", err)
	}
	return svc, nil
}

// openBrokerDecisionLog opens (or creates) the broker-decision log
// file at the canonical path and returns a DecisionWriter ready to
// append. The file is opened append-only with mode 0600 so only
// the broker's owner can read/write it; the parent dir is created
// if missing.
//
// If the file can't be opened for any reason (parent-dir creation
// fails, etc.), returns the error. The caller surfaces it as a
// fatal broker-startup failure rather than silently falling back
// to MemoryWriter — losing the decision log defeats the purpose.
func openBrokerDecisionLog(cfg *config.Config) (broker.DecisionWriter, error) {
	path := brokerDecisionLogPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return broker.NewJSONLWriter(f), nil
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
