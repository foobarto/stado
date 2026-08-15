package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testTreeDigest = "0123456789abcdef0123456789abcdef01234567"

func activeVerificationWorker(t *testing.T, f *fixture, auth Authority) WorkerRun {
	t.Helper()
	requested, err := f.service.RequestWorkerRun(context.Background(), auth, WorkerRunRequest{
		RunID: "run-1", Objective: "do the work", Prompt: "continue", Conflict: WorkerRunRejectOperatorLoop,
	}, "worker-request")
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{
		RunID: requested.RunID, ExpectedVersion: requested.Version,
	}, "worker-activate")
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func publishVerificationTurn(t *testing.T, f *fixture) Event {
	t.Helper()
	payload := json.RawMessage(`{"schema":"stado.dev/session-turn-facts/v1","anchor":{"session_sequence":4294967297,"turn_ref":"git:refs/stado/sessions/s1/tree@` + testTreeDigest + `#turn-1-iteration-1","tree_digest":"` + testTreeDigest + `"}}`)
	event, err := f.service.PublishEvent(context.Background(), SessionScope{SessionID: "session-1", Generation: 3}, EventInput{
		ID: "turn-1", Kind: turnCommittedEvent, Data: payload, EvidenceRefs: []string{"git:tree@abc"},
	}, "turn-publish")
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestVerificationRequestWaitsForSourceAckAndPersistsOnlyFacts(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#verification")
	worker := activeVerificationWorker(t, f, auth)
	turn := publishVerificationTurn(t, f)

	input := VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version, SourceEventSequence: turn.WALSequence}
	requested, err := f.service.RequestVerification(context.Background(), auth, input, "verify-request")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.RequestVerification(context.Background(), auth, input, "verify-request")
	if err != nil || retry.ID != requested.ID || retry.WALSequence != requested.WALSequence {
		t.Fatalf("request retry=%+v err=%v", retry, err)
	}
	if requested.Status != VerificationRequested || requested.Source.EventSequence != turn.WALSequence || requested.Source.SessionSequence != 4294967297 || requested.Source.TreeDigest != testTreeDigest || len(requested.SourceEvidenceRefs) != 1 {
		t.Fatalf("requested=%+v", requested)
	}

	claim := VerificationClaim{ID: requested.ID, ExpectedVersion: requested.Version, SuiteDigest: testDigest, CommandDigests: []string{testDigest}}
	if _, err := f.service.ClaimVerification(context.Background(), auth, claim, "claim"); !errors.Is(err, ErrNotDue) {
		t.Fatalf("claim before source ack error=%v", err)
	}
	if _, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: turn.WALSequence}, "ack-turn"); err != nil {
		t.Fatal(err)
	}
	running, err := f.service.ClaimVerification(context.Background(), auth, claim, "claim")
	if err != nil || running.Status != VerificationRunning || running.Version != 2 {
		t.Fatalf("running=%+v err=%v", running, err)
	}

	finish := VerificationFinish{
		ID: running.ID, ExpectedVersion: running.Version, Outcome: VerificationCommandsSucceeded,
		Commands:     []VerificationCommandFact{{Ordinal: 1, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "succeeded", EvidenceRefs: []string{"audit:verification:1"}}},
		EvidenceRefs: []string{"audit:verification:1"},
	}
	terminal, err := f.service.FinishVerification(context.Background(), auth, finish, "finish")
	if err != nil || terminal.Status != VerificationTerminal || terminal.Outcome != VerificationCommandsSucceeded || terminal.Version != 3 {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
	wantWALRef := verificationWALEvidenceRef(terminal.WALSequence)
	if !reflect.DeepEqual(terminal.EvidenceRefs, []string{"audit:verification:1", wantWALRef}) || !reflect.DeepEqual(terminal.Commands[0].EvidenceRefs, []string{"audit:verification:1", wantWALRef}) {
		t.Fatalf("terminal evidence refs=%v command=%v", terminal.EvidenceRefs, terminal.Commands[0].EvidenceRefs)
	}
	retryTerminal, err := f.service.FinishVerification(context.Background(), auth, finish, "finish")
	if err != nil || !reflect.DeepEqual(retryTerminal, terminal) {
		t.Fatalf("finish replay=%+v want=%+v err=%v", retryTerminal, terminal, err)
	}

	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true})
	if err != nil || len(projection.Verifications) != 1 || !reflect.DeepEqual(projection.Verifications[0], terminal) {
		t.Fatalf("projection=%+v err=%v", projection.Verifications, err)
	}
	pending, _, err := f.service.PendingEvents(context.Background(), auth, []string{VerificationFinishedEvent}, 10)
	if err != nil || len(pending) != 1 || pending[0].TargetPlugin != auth.PluginID {
		t.Fatalf("terminal event=%+v err=%v", pending, err)
	}
	if !reflect.DeepEqual(pending[0].EvidenceRefs, terminal.EvidenceRefs) {
		t.Fatalf("terminal event evidence=%v want=%v", pending[0].EvidenceRefs, terminal.EvidenceRefs)
	}
	var eventShape map[string]json.RawMessage
	if err := json.Unmarshal(pending[0].Data, &eventShape); err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]bool{
		"schema": true, "verification_id": true, "run_id": true, "version": true,
		"source": true, "source_evidence_refs": true, "suite_digest": true, "command_digests": true,
		"outcome": true, "commands": true, "evidence_refs": true,
	}
	if len(eventShape) != len(wantKeys) {
		t.Fatalf("terminal event shape keys=%v", reflect.ValueOf(eventShape).MapKeys())
	}
	for key := range eventShape {
		if !wantKeys[key] {
			t.Fatalf("terminal event leaked internal/unknown field %q: %s", key, pending[0].Data)
		}
	}
	for _, forbidden := range []string{"session_id", "generation", "plugin_id", "owner", "worker_version", "wal_sequence", "status", "created_at", "updated_at"} {
		if _, ok := eventShape[forbidden]; ok {
			t.Fatalf("terminal event leaked internal authority field %q", forbidden)
		}
	}
	var result VerificationResultEventV1
	if err := json.Unmarshal(pending[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.SourceEvidenceRefs, requested.SourceEvidenceRefs) || !reflect.DeepEqual(result.EvidenceRefs, terminal.EvidenceRefs) {
		t.Fatalf("result source/terminal evidence=%v/%v", result.SourceEvidenceRefs, result.EvidenceRefs)
	}

	raw, err := json.Marshal(f.store.Records())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"go test ./...", "sensitive command output"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("WAL persisted forbidden verification plaintext %q", secret)
		}
	}

	reloaded := New(f.store)
	got, err := reloaded.VerificationByID(context.Background(), auth, terminal.ID)
	if err != nil || !reflect.DeepEqual(got, terminal) {
		t.Fatalf("reloaded=%+v err=%v", got, err)
	}
}

func TestVerificationResultV1CrossRepositoryFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "runtime", "testdata", "session-verification-facts-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var result VerificationResultEventV1
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("fixture has trailing JSON: %v", err)
	}
	finish := VerificationFinish{
		ID: result.VerificationID, ExpectedVersion: result.Version - 1,
		Outcome: result.Outcome, FailureKind: result.FailureKind,
		FailureFingerprint: result.FailureFingerprint,
		Commands:           result.Commands, EvidenceRefs: result.EvidenceRefs,
	}
	if result.Schema != VerificationResultSchemaV1 || result.Source.EventSequence != 42 || result.Source.TreeDigest != testTreeDigest || len(result.SourceEvidenceRefs) != 1 || validateVerificationFinishShape(finish, DefaultLimits()) != nil {
		t.Fatalf("fixture contract=%+v", result)
	}
	for _, forbidden := range []string{"session_id", "generation", "plugin_id", "owner", "worker_version", "wal_sequence", "created_at", "updated_at"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Fatalf("fixture leaked authority field %q", forbidden)
		}
	}
}

func TestVerificationRequestRejectsAcknowledgedWrongOrInactiveSources(t *testing.T) {
	t.Run("acknowledged", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#verification")
		worker := activeVerificationWorker(t, f, auth)
		turn := publishVerificationTurn(t, f)
		if _, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: turn.WALSequence}, "ack"); err != nil {
			t.Fatal(err)
		}
		_, err := f.service.RequestVerification(context.Background(), auth, VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version, SourceEventSequence: turn.WALSequence}, "request")
		if !errors.Is(err, ErrVersion) {
			t.Fatalf("acknowledged source error=%v", err)
		}
	})

	t.Run("wrong event kind", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#verification")
		worker := activeVerificationWorker(t, f, auth)
		event, err := f.service.PublishEvent(context.Background(), SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}, EventInput{Kind: "session.other", Data: json.RawMessage(`{}`)}, "other")
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.service.RequestVerification(context.Background(), auth, VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version, SourceEventSequence: event.WALSequence}, "request")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("wrong source error=%v", err)
		}
	})

	t.Run("stale worker version", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#verification")
		worker := activeVerificationWorker(t, f, auth)
		turn := publishVerificationTurn(t, f)
		_, err := f.service.RequestVerification(context.Background(), auth, VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version + 1, SourceEventSequence: turn.WALSequence}, "request")
		if !errors.Is(err, ErrVersion) {
			t.Fatalf("stale worker error=%v", err)
		}
	})
}

func TestVerificationNoSuiteIsASeparateFactualOutcome(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#verification")
	worker := activeVerificationWorker(t, f, auth)
	turn := publishVerificationTurn(t, f)
	requested, err := f.service.RequestVerification(context.Background(), auth, VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version, SourceEventSequence: turn.WALSequence}, "request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: turn.WALSequence}, "ack"); err != nil {
		t.Fatal(err)
	}
	running, err := f.service.ClaimVerification(context.Background(), auth, VerificationClaim{ID: requested.ID, ExpectedVersion: requested.Version, SuiteDigest: testDigest}, "claim")
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := f.service.FinishVerification(context.Background(), auth, VerificationFinish{ID: running.ID, ExpectedVersion: running.Version, Outcome: VerificationNoSuite}, "finish")
	if err != nil || terminal.Outcome != VerificationNoSuite || len(terminal.Commands) != 0 {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
}

func runningVerification(t *testing.T, commandDigests []string) (*fixture, Authority, WorkerRun, Verification) {
	t.Helper()
	f := newFixture(t)
	auth := testAuthority("plugin#verification")
	worker := activeVerificationWorker(t, f, auth)
	turn := publishVerificationTurn(t, f)
	requested, err := f.service.RequestVerification(context.Background(), auth, VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version, SourceEventSequence: turn.WALSequence}, "request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: turn.WALSequence}, "ack"); err != nil {
		t.Fatal(err)
	}
	running, err := f.service.ClaimVerification(context.Background(), auth, VerificationClaim{ID: requested.ID, ExpectedVersion: requested.Version, SuiteDigest: testDigest, CommandDigests: commandDigests}, "claim")
	if err != nil {
		t.Fatal(err)
	}
	return f, auth, worker, running
}

func TestVerificationWorkerTerminalStateWinsOverCommandSuccess(t *testing.T) {
	f, auth, worker, running := runningVerification(t, []string{testDigest})
	if _, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{
		RunID: worker.RunID, ExpectedVersion: worker.Version, Status: WorkerRunInterrupted,
		Reason: "operator paused", ControlSequence: 99,
	}, "worker-terminal"); err != nil {
		t.Fatal(err)
	}
	terminal, err := f.service.FinishVerification(context.Background(), auth, VerificationFinish{
		ID: running.ID, ExpectedVersion: running.Version, Outcome: VerificationCommandsSucceeded,
		Commands: []VerificationCommandFact{{Ordinal: 1, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "succeeded"}},
	}, "finish")
	if err != nil || terminal.Outcome != VerificationCancelled || terminal.FailureKind != "worker_terminal" || terminal.Commands[0].Outcome != "succeeded" {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
}

func TestVerificationRejectsContradictoryCommandFactSequences(t *testing.T) {
	succeeded := func(ordinal int) VerificationCommandFact {
		return VerificationCommandFact{Ordinal: ordinal, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "succeeded"}
	}
	notRun := func(ordinal int) VerificationCommandFact {
		return VerificationCommandFact{Ordinal: ordinal, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "not_run"}
	}
	failed := func(ordinal int) VerificationCommandFact {
		return VerificationCommandFact{Ordinal: ordinal, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "failed", FailureKind: "command_failed", FailureFingerprint: testDigest}
	}
	tests := []struct {
		name    string
		outcome VerificationOutcome
		kind    string
		facts   []VerificationCommandFact
	}{
		{name: "success with skipped suffix", outcome: VerificationCommandsSucceeded, facts: []VerificationCommandFact{succeeded(1), notRun(2)}},
		{name: "success after terminal", outcome: VerificationCommandFailed, kind: "command_failed", facts: []VerificationCommandFact{failed(1), succeeded(2)}},
		{name: "two terminal failures", outcome: VerificationCommandFailed, kind: "command_failed", facts: []VerificationCommandFact{failed(1), failed(2)}},
		{name: "failed fact without typed fingerprint", outcome: VerificationCommandFailed, kind: "command_failed", facts: []VerificationCommandFact{{Ordinal: 1, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "failed"}, notRun(2)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, auth, _, running := runningVerification(t, []string{testDigest, testDigest})
			finish := VerificationFinish{ID: running.ID, ExpectedVersion: running.Version, Outcome: tc.outcome, FailureKind: tc.kind, Commands: tc.facts}
			if tc.kind != "" {
				finish.FailureFingerprint = testDigest
			}
			if _, err := f.service.FinishVerification(context.Background(), auth, finish, "finish"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("contradictory finish error=%v", err)
			}
		})
	}
}

func TestVerificationZeroCommandCancellationAndRecoveryCanSettle(t *testing.T) {
	tests := []struct {
		name        string
		outcome     VerificationOutcome
		failureKind string
		terminalize bool
	}{
		{name: "stale anchor", outcome: VerificationCancelled, failureKind: "stale_anchor"},
		{name: "changed suite", outcome: VerificationInfrastructure, failureKind: "suite_changed"},
		{name: "worker terminal", outcome: VerificationNoSuite, terminalize: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, auth, worker, running := runningVerification(t, nil)
			if tc.terminalize {
				if _, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{RunID: worker.RunID, ExpectedVersion: worker.Version, Status: WorkerRunInterrupted, Reason: "paused", ControlSequence: 100}, "terminal"); err != nil {
					t.Fatal(err)
				}
			}
			terminal, err := f.service.FinishVerification(context.Background(), auth, VerificationFinish{
				ID: running.ID, ExpectedVersion: running.Version, Outcome: tc.outcome,
				FailureKind: tc.failureKind, FailureFingerprint: func() string {
					if tc.failureKind == "" {
						return ""
					}
					return testDigest
				}(),
			}, "finish")
			if err != nil || terminal.Status != VerificationTerminal {
				t.Fatalf("terminal=%+v err=%v", terminal, err)
			}
			if tc.terminalize && (terminal.Outcome != VerificationCancelled || terminal.FailureKind != "worker_terminal") {
				t.Fatalf("worker precedence terminal=%+v", terminal)
			}
		})
	}
}

func appendCorruptVerification(t *testing.T, f *fixture, auth Authority, eventType string, verification Verification) {
	t.Helper()
	raw, err := json.Marshal(eventEnvelope{
		Meta:         eventMeta{Schema: eventSchema, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, RequestDigest: "tampered-digest"},
		Verification: &verification,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Append(wal.Transaction{
		ID: "tampered-" + eventType + "-" + time.Now().String(), IdempotencyKey: "tampered:" + eventType + ":" + time.Now().String(),
		Principal: auth.Principal, Actor: auth.Actor,
		Events: []wal.Event{{Store: storeName, Type: eventType, Session: auth.SessionID, Data: raw}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerificationFoldRejectsTamperedRunningAndTerminalFacts(t *testing.T) {
	t.Run("source evidence changed", func(t *testing.T) {
		f, auth, _, running := runningVerification(t, []string{testDigest})
		running.Version++
		running.Status = VerificationTerminal
		running.Outcome = VerificationCommandsSucceeded
		running.Commands = []VerificationCommandFact{{Ordinal: 1, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "succeeded"}}
		running.SourceEvidenceRefs = []string{"forged:source"}
		appendCorruptVerification(t, f, auth, "verification.terminal", running)
		if _, err := f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true}); err == nil || !strings.Contains(err.Error(), "identity or version") {
			t.Fatalf("tampered source refs fold error=%v", err)
		}
	})

	t.Run("invalid claimed digest", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#verification")
		worker := activeVerificationWorker(t, f, auth)
		turn := publishVerificationTurn(t, f)
		requested, err := f.service.RequestVerification(context.Background(), auth, VerificationRequest{RunID: worker.RunID, ExpectedWorkerVersion: worker.Version, SourceEventSequence: turn.WALSequence}, "request")
		if err != nil {
			t.Fatal(err)
		}
		running := requested
		running.Version++
		running.Status = VerificationRunning
		running.SuiteDigest = testDigest
		running.CommandDigests = []string{"not-a-digest"}
		appendCorruptVerification(t, f, auth, "verification.running", running)
		if _, err := f.service.Project(context.Background(), auth, ProjectionOptions{}); err == nil || !strings.Contains(err.Error(), "command digest") {
			t.Fatalf("tampered command digest fold error=%v", err)
		}
	})

	t.Run("malformed terminal result digest", func(t *testing.T) {
		f, auth, _, running := runningVerification(t, []string{testDigest})
		terminal := running
		terminal.Version++
		terminal.Status = VerificationTerminal
		terminal.Outcome = VerificationCommandsSucceeded
		terminal.Commands = []VerificationCommandFact{{Ordinal: 1, CommandDigest: testDigest, ResultDigest: "bad", Outcome: "succeeded"}}
		appendCorruptVerification(t, f, auth, "verification.terminal", terminal)
		if _, err := f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true}); err == nil || !strings.Contains(err.Error(), "terminal facts") {
			t.Fatalf("tampered terminal fact fold error=%v", err)
		}
	})

	t.Run("contradictory terminal sequence", func(t *testing.T) {
		f, auth, _, running := runningVerification(t, []string{testDigest, testDigest})
		terminal := running
		terminal.Version++
		terminal.Status = VerificationTerminal
		terminal.Outcome = VerificationCommandsSucceeded
		terminal.Commands = []VerificationCommandFact{
			{Ordinal: 1, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "succeeded"},
			{Ordinal: 2, CommandDigest: testDigest, ResultDigest: testDigest, Outcome: "not_run"},
		}
		appendCorruptVerification(t, f, auth, "verification.terminal", terminal)
		if _, err := f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true}); err == nil || !strings.Contains(err.Error(), "terminal facts") {
			t.Fatalf("contradictory terminal fold error=%v", err)
		}
	})
}
