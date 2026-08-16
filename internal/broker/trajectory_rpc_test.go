package broker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	toolOutcome := func(invocation int, argsDigest string) error {
		raw, err := json.Marshal(SessionContextToolOutcomeParams{
			SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
			Turn: 3, Invocation: invocation, CallID: "shell", Tool: "shell", ArgsDigest: argsDigest,
		})
		if err != nil {
			return err
		}
		_, err = service.Dispatch(context.Background(), MethodSessionContextToolOutcome, raw)
		return err
	}
	for _, invocation := range []int{0, 1} {
		if err := toolOutcome(invocation, digest); err != nil {
			t.Fatal(err)
		}
	}
	if err := toolOutcome(1, digest); err != nil {
		t.Fatalf("idempotent outcome retry: %v", err)
	}
	if err := toolOutcome(1, strings.Repeat("b", 64)); dispatchCode(err) != ErrCodeInvalidParams {
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

func TestSessionContextReadRPCAuthenticatesControllerAndRecoveryBearer(t *testing.T) {
	service, _ := openScopeService(t, t.TempDir())
	defer service.Close()
	handle, credential := createDurableScope(t, service, t.TempDir(), "logical-session-a")
	dispatchRPC(t, service, MethodSessionContextObjective, SessionContextObjectiveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken, Objective: "ship safely",
	})

	controllerRaw := dispatchRPC(t, service, MethodSessionContextState, SessionContextStateParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
	})
	var controllerState sessioncontext.State
	if err := json.Unmarshal(controllerRaw, &controllerState); err != nil {
		t.Fatal(err)
	}
	if controllerState.SessionID != credential.Subject || controllerState.Objective != "ship safely" {
		t.Fatalf("controller projection=%+v", controllerState)
	}

	recoveryAuth := SessionContextReadAuth{
		Subject: credential.Subject, Ticket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	}
	recoveryRaw := dispatchRPC(t, service, MethodSessionContextState, SessionContextStateParams(recoveryAuth))
	var recoveryState sessioncontext.State
	if err := json.Unmarshal(recoveryRaw, &recoveryState); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recoveryState, controllerState) {
		t.Fatalf("recovery projection differs from controller projection: recovery=%+v controller=%+v", recoveryState, controllerState)
	}

	journalRaw := dispatchRPC(t, service, MethodSessionContextJournal, SessionContextJournalParams{
		SessionContextReadAuth: recoveryAuth, Limit: 1,
	})
	var journal []sessioncontext.JournalEntry
	if err := json.Unmarshal(journalRaw, &journal); err != nil {
		t.Fatal(err)
	}
	if len(journal) != 1 || journal[0].Type != "state.updated" {
		t.Fatalf("journal=%+v", journal)
	}
}

func TestSessionContextReadRPCRejectsSubstitutedOrMixedAuthority(t *testing.T) {
	service, _ := openScopeService(t, t.TempDir())
	defer service.Close()
	handle, credential := createDurableScope(t, service, t.TempDir(), "logical-session-a")

	wrongResume := credential.ResumeSecret[:len(credential.ResumeSecret)-1] + "0"
	if wrongResume == credential.ResumeSecret {
		wrongResume = credential.ResumeSecret[:len(credential.ResumeSecret)-1] + "1"
	}
	wrong := SessionContextStateParams{
		Subject: credential.Subject, Ticket: credential.Ticket, ResumeSecret: wrongResume,
	}
	raw, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispatch(context.Background(), MethodSessionContextState, raw); dispatchCode(err) != ErrCodeSessionScopeCredential {
		t.Fatalf("wrong recovery bearer error=%v", err)
	}

	mixed := SessionContextStateParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		Subject: credential.Subject, Ticket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	}
	raw, err = json.Marshal(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispatch(context.Background(), MethodSessionContextState, raw); dispatchCode(err) != ErrCodeInvalidParams {
		t.Fatalf("mixed read authority error=%v", err)
	}

	tooLarge := SessionContextJournalParams{
		SessionContextReadAuth: SessionContextReadAuth{SessionID: handle.SessionID, ControllerToken: handle.controllerToken},
		Limit:                  maxSessionJournalEntries + 1,
	}
	raw, err = json.Marshal(tooLarge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispatch(context.Background(), MethodSessionContextJournal, raw); dispatchCode(err) != ErrCodeInvalidParams {
		t.Fatalf("unbounded journal error=%v", err)
	}
}

func TestSessionContextRecoveryReadSurvivesBrokerRestart(t *testing.T) {
	walDir := t.TempDir()
	first, _ := openScopeService(t, walDir)
	handle, credential := createDurableScope(t, first, t.TempDir(), "logical-session-a")
	dispatchRPC(t, first, MethodSessionContextObjective, SessionContextObjectiveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken, Objective: "survive restart",
	})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _ := openScopeService(t, walDir)
	defer second.Close()
	raw := dispatchRPC(t, second, MethodSessionContextState, SessionContextStateParams{
		Subject: credential.Subject, Ticket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	})
	var state sessioncontext.State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.SessionID != credential.Subject || state.Objective != "survive restart" {
		t.Fatalf("restarted projection=%+v", state)
	}
}

func dispatchCode(err error) int {
	if dispatchErr, ok := err.(*DispatchError); ok {
		return dispatchErr.Code
	}
	return 0
}
