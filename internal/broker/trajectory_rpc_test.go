package broker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/sessioncontext"
)

type failingTrajectoryAppender struct{}

func (failingTrajectoryAppender) Append(wal.Transaction) (wal.AppendResult, error) {
	return wal.AppendResult{}, errors.New("write failed")
}

func (failingTrajectoryAppender) Records() []wal.Record { return nil }

func TestTrajectoryRPCDerivesDurableAuthorityAndSignals(t *testing.T) {
	service, store := openScopeService(t, t.TempDir())
	defer service.Close()
	handle, _ := createDurableScope(t, service, t.TempDir(), "logical-session-a")

	dispatchRPC(t, service, MethodSessionContextObjective, SessionContextObjectiveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken, Objective: "ship safely",
	})
	digest := strings.Repeat("a", 64)
	toolOutcome := func(callID, argsDigest string) error {
		raw, err := json.Marshal(SessionContextToolOutcomeParams{
			SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
			Turn: 3, CallID: callID, Tool: "shell", ArgsDigest: argsDigest,
		})
		if err != nil {
			return err
		}
		_, err = service.Dispatch(context.Background(), MethodSessionContextToolOutcome, raw)
		return err
	}
	for _, callID := range []string{"call-1", "call-2"} {
		if err := toolOutcome(callID, digest); err != nil {
			t.Fatal(err)
		}
	}
	if err := toolOutcome("call-2", digest); err != nil {
		t.Fatalf("idempotent outcome retry: %v", err)
	}
	if err := toolOutcome("call-2", strings.Repeat("b", 64)); dispatchCode(err) != ErrCodeInvalidParams {
		t.Fatalf("conflicting outcome retry: %v", err)
	}

	projection := sessioncontext.New(store)
	state, err := projection.State("logical-session-a")
	if err != nil {
		t.Fatal(err)
	}
	if state.Objective != "ship safely" {
		t.Fatalf("state=%+v", state)
	}
	signals, err := projection.Signals("logical-session-a", false)
	if err != nil || len(signals) != 1 || signals[0].Type != sessioncontext.SignalRepeatedToolFailure {
		t.Fatalf("signals=%v err=%v", signals, err)
	}

	service.sessionsMu.RLock()
	wantPrincipal := service.sessions[handle.SessionID].principal
	service.sessionsMu.RUnlock()
	trajectoryRecords := 0
	for _, record := range store.Records() {
		if record.Transaction.Actor != "broker:trajectory" {
			continue
		}
		trajectoryRecords++
		if record.Transaction.Principal != wantPrincipal {
			t.Fatalf("principal=%q, want broker-derived %q", record.Transaction.Principal, wantPrincipal)
		}
		for _, event := range record.Transaction.Events {
			if event.Session != "logical-session-a" {
				t.Fatalf("event session=%q", event.Session)
			}
		}
	}
	if trajectoryRecords != 3 {
		t.Fatalf("trajectory records=%d, want 3", trajectoryRecords)
	}
}

func TestTrajectoryRPCRejectsAuthoritySubstitution(t *testing.T) {
	service, _ := openScopeService(t, t.TempDir())
	defer service.Close()
	handle, _ := createDurableScope(t, service, t.TempDir(), "logical-session-a")

	raw, err := json.Marshal(map[string]any{
		"session_id": handle.SessionID, "controller_token": handle.controllerToken,
		"objective": "poison global memory", "subject": "other-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispatch(context.Background(), MethodSessionContextObjective, raw); dispatchCode(err) != ErrCodeInvalidParams || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("subject substitution error=%v", err)
	}

	params, err := json.Marshal(SessionContextObjectiveParams{
		SessionID: handle.SessionID, ControllerToken: "wrong", Objective: "poison global memory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispatch(context.Background(), MethodSessionContextObjective, params); dispatchCode(err) != ErrCodeInvalidParams || !strings.Contains(err.Error(), ErrSessionController.Error()) {
		t.Fatalf("controller substitution error=%v", err)
	}
}

func TestTrajectoryRPCRequiresDurableLogicalSession(t *testing.T) {
	service, _ := openScopeService(t, t.TempDir())
	defer service.Close()
	handle, decision, err := service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSession handle=%+v decision=%+v err=%v", handle, decision, err)
	}
	raw, err := json.Marshal(SessionContextObjectiveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken, Objective: "ship safely",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Dispatch(context.Background(), MethodSessionContextObjective, raw)
	if dispatchCode(err) != ErrCodeInvalidParams || !strings.Contains(err.Error(), "durable logical session") {
		t.Fatalf("non-durable error=%v", err)
	}
}

func TestTrajectoryRPCReportsCanonicalStoreFailureAsInternal(t *testing.T) {
	service, _ := openScopeService(t, t.TempDir())
	defer service.Close()
	handle, _ := createDurableScope(t, service, t.TempDir(), "logical-session-a")
	service.sessionContext = sessioncontext.New(failingTrajectoryAppender{})
	raw, err := json.Marshal(SessionContextObjectiveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken, Objective: "ship safely",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Dispatch(context.Background(), MethodSessionContextObjective, raw)
	if dispatchCode(err) != ErrCodeInternal || !strings.Contains(err.Error(), "canonical session context") {
		t.Fatalf("store failure error=%v", err)
	}
}

func dispatchCode(err error) int {
	if dispatchErr, ok := err.(*DispatchError); ok {
		return dispatchErr.Code
	}
	return 0
}
