package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/broker/wal"
)

func configureExactHandoffLineage(t *testing.T, service *Service, source, child, turnRef string) {
	t.Helper()
	if err := service.ConfigureSessionLineageVerifier(SessionLineageVerifierFunc(func(_ context.Context, check SessionLineageCheck) error {
		if check.SourceCWD == "" || check.SourceSubject != source || check.ChildSubject != child || check.SourceTurnRef != turnRef {
			return fmt.Errorf("unexpected lineage check: %+v", check)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
}

func handoffCredential(source SessionAdoptionCredential, child string) SessionAdoptionCredential {
	source.Subject = child
	return source
}

func TestSessionSubjectHandoffPreservesWholeApplicationScopeAndFencesSource(t *testing.T) {
	service, store := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	cwd := t.TempDir()
	const source, child = "logical-session-source", "logical-session-child"
	turnRef := "refs/sessions/" + source + "/turns/3"
	configureExactHandoffLineage(t, service, source, child, turnRef)
	handle, credential := createDurableScope(t, service, cwd, source)

	auth := application.Authority{
		SessionID: handle.SessionID, Generation: 1, PluginID: "example.test/application",
		Principal: "os-user:test", Actor: "plugin:example.test/application@v1",
	}
	if _, err := service.artifacts.application.AppendJournal(context.Background(), auth, application.JournalAppend{
		ID: "checkpoint", RunID: "run-1", Kind: "checkpoint", Summary: "before handoff",
		Data: json.RawMessage(`{"turn":3}`),
	}, "handoff-journal"); err != nil {
		t.Fatal(err)
	}
	run, err := service.artifacts.application.RequestWorkerRun(context.Background(), auth, application.WorkerRunRequest{
		RunID: "run-1", Objective: "finish", Prompt: "continue", Conflict: application.WorkerRunRejectOperatorLoop,
	}, "handoff-worker")
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.artifacts.application.ActivateWorkerRun(context.Background(), auth, application.WorkerRunCAS{
		RunID: run.RunID, ExpectedVersion: run.Version,
	}, "handoff-worker-activate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.artifacts.application.AcquireHold(context.Background(), auth, application.HoldAcquire{
		ID: "hold-1", RunID: run.RunID, ReasonCode: "quality-gate", TTL: time.Hour,
	}, "handoff-hold"); err != nil {
		t.Fatal(err)
	}
	service.artifacts.mu.Lock()
	service.artifacts.bindings["old-binding"] = artifactBinding{
		token: "old-binding", sessionID: handle.SessionID, generation: 1, controllerVersion: 1,
	}
	service.artifacts.mu.Unlock()

	reservation, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, child, turnRef)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.SourceSubject != source || reservation.ChildSubject != child || reservation.SourceTurnRef != turnRef {
		t.Fatalf("reservation=%+v", reservation)
	}
	childCredential := handoffCredential(credential, child)
	moved, stable, err := service.CommitSessionSubjectHandoff(
		context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential,
	)
	if err != nil {
		t.Fatal(err)
	}
	if moved.SessionID != handle.SessionID || moved.subject != child || moved.controllerToken == handle.controllerToken || stable != childCredential {
		t.Fatalf("moved=%+v stable=%+v", moved, stable)
	}
	if err := service.authenticateSessionController(handle.SessionID, handle.controllerToken); !errors.Is(err, ErrSessionController) {
		t.Fatalf("source controller survived handoff: %v", err)
	}
	if _, err := service.artifactBinding("old-binding"); err == nil {
		t.Fatal("pre-handoff application binding survived controller rotation")
	}
	projection, err := service.artifacts.application.Project(context.Background(), auth, application.ProjectionOptions{
		JournalLimit: 8, WorkerLimit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Journal) != 1 || len(projection.WorkerRuns) != 1 || len(projection.Holds) != 1 ||
		projection.WorkerRuns[0].RunID != run.RunID {
		t.Fatalf("application scope changed during subject handoff: %+v", projection)
	}
	if _, _, err := service.AdoptSession(credential, cwd); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("old subject remained adoptable: %v", err)
	}
	if err := service.DetachSession(moved.SessionID, moved.controllerToken); err != nil {
		t.Fatal(err)
	}
	adopted, _, err := service.AdoptSession(childCredential, cwd)
	if err != nil || adopted.SessionID != handle.SessionID {
		t.Fatalf("child adoption=%+v err=%v", adopted, err)
	}

	records, err := json.Marshal(store.Records())
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{
		"source controller": handle.controllerToken, "moved controller": moved.controllerToken,
		"ticket": credential.Ticket, "resume": credential.ResumeSecret,
	} {
		if strings.Contains(string(records), secret) {
			t.Fatalf("handoff WAL contains plaintext %s", name)
		}
	}
}

func TestSessionSubjectHandoffCrashBeforeCommitLeavesSourceAuthoritative(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	cwd := t.TempDir()
	const source, child = "logical-session-source", "logical-session-child"
	turnRef := "refs/sessions/" + source + "/turns/1"
	first, _ := openScopeService(t, walDir)
	configureExactHandoffLineage(t, first, source, child, turnRef)
	handle, credential := createDurableScope(t, first, cwd, source)
	reservation, err := first.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, child, turnRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _ := openScopeService(t, walDir)
	defer second.Close()
	configureExactHandoffLineage(t, second, source, child, turnRef)
	childCredential := handoffCredential(credential, child)
	if _, _, err := second.AdoptSession(childCredential, cwd); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("staged child became authoritative before commit: %v", err)
	}
	adopted, _, err := second.AdoptSession(credential, cwd)
	if err != nil {
		t.Fatalf("source could not recover after pre-commit crash: %v", err)
	}
	if _, _, err := second.CommitSessionSubjectHandoff(context.Background(), adopted.SessionID, adopted.controllerToken, reservation.ID, childCredential); !errors.Is(err, ErrSessionHandoffConflict) {
		t.Fatalf("pre-restart reservation raced adopted controller version: %v", err)
	}
	replacement, err := second.ReserveSessionSubjectHandoff(context.Background(), adopted.SessionID, adopted.controllerToken, child, turnRef)
	if err != nil || replacement.ID == reservation.ID {
		t.Fatalf("replacement reservation=%+v err=%v", replacement, err)
	}
}

func TestSessionSubjectHandoffCrashAfterCommitRestoresChildAndApplicationState(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	cwd := t.TempDir()
	const source, child = "logical-session-source", "logical-session-child"
	turnRef := "refs/sessions/" + source + "/turns/2"
	first, _ := openScopeService(t, walDir)
	configureExactHandoffLineage(t, first, source, child, turnRef)
	handle, credential := createDurableScope(t, first, cwd, source)
	auth := application.Authority{
		SessionID: handle.SessionID, Generation: 1, PluginID: "example.test/application",
		Principal: "os-user:test", Actor: "plugin:example.test/application@v1",
	}
	if _, err := first.artifacts.application.AppendJournal(context.Background(), auth, application.JournalAppend{
		ID: "before-crash", RunID: "run-crash", Kind: "checkpoint", Summary: "durable",
	}, "handoff-crash-journal"); err != nil {
		t.Fatal(err)
	}
	reservation, err := first.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, child, turnRef)
	if err != nil {
		t.Fatal(err)
	}
	childCredential := handoffCredential(credential, child)
	if _, _, err := first.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _ := openScopeService(t, walDir)
	defer second.Close()
	configureExactHandoffLineage(t, second, source, child, turnRef)
	if _, _, err := second.AdoptSession(credential, cwd); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("old source adopted after committed crash: %v", err)
	}
	adopted, _, err := second.AdoptSession(childCredential, cwd)
	if err != nil || adopted.SessionID != handle.SessionID {
		t.Fatalf("child crash adoption=%+v err=%v", adopted, err)
	}
	projection, err := second.artifacts.application.Project(context.Background(), auth, application.ProjectionOptions{JournalLimit: 8})
	if err != nil || len(projection.Journal) != 1 || projection.Journal[0].ID != "before-crash" {
		t.Fatalf("post-handoff restart projection=%+v err=%v", projection, err)
	}
}

func TestSessionSubjectHandoffLostCommitReplyReplaysPriorController(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			walDir := filepath.Join(t.TempDir(), "wal")
			cwd := t.TempDir()
			const source, child = "logical-session-source", "logical-session-child"
			turnRef := "refs/sessions/" + source + "/turns/1"
			service, _ := openScopeService(t, walDir)
			configureExactHandoffLineage(t, service, source, child, turnRef)
			handle, credential := createDurableScope(t, service, cwd, source)
			reservation, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, child, turnRef)
			if err != nil {
				t.Fatal(err)
			}
			childCredential := handoffCredential(credential, child)
			lost, _, err := service.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate the broker committing and the reply disappearing before the
			// native client learns the newly minted controller.
			if restart {
				if err := service.Close(); err != nil {
					t.Fatal(err)
				}
				service, _ = openScopeService(t, walDir)
				defer service.Close()
				configureExactHandoffLineage(t, service, source, child, turnRef)
			} else {
				defer service.Close()
			}
			replayed, _, err := service.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential)
			if err != nil {
				t.Fatalf("exact lost-reply replay: %v", err)
			}
			if replayed.SessionID != handle.SessionID || replayed.subject != child || replayed.controllerToken == lost.controllerToken {
				t.Fatalf("lost=%+v replayed=%+v", lost, replayed)
			}
			if err := service.authenticateSessionController(handle.SessionID, lost.controllerToken); !errors.Is(err, ErrSessionController) {
				t.Fatalf("lost controller survived replay: %v", err)
			}
			if _, _, err := service.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential); !errors.Is(err, ErrSessionHandoffConflict) {
				t.Fatalf("committed reservation replayed more than once: %v", err)
			}
		})
	}
}

func TestSessionSubjectHandoffLostReplyReplayExpires(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	cwd := t.TempDir()
	const source, child = "logical-session-source", "logical-session-child"
	turnRef := "refs/sessions/" + source + "/turns/1"
	configureExactHandoffLineage(t, service, source, child, turnRef)
	handle, credential := createDurableScope(t, service, cwd, source)
	reservation, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, child, turnRef)
	if err != nil {
		t.Fatal(err)
	}
	childCredential := handoffCredential(credential, child)
	if _, _, err := service.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential); err != nil {
		t.Fatal(err)
	}
	now = now.Add(sessionHandoffTTL + time.Second)
	if _, _, err := service.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, childCredential); !errors.Is(err, ErrSessionHandoffConflict) {
		t.Fatalf("expired lost-reply replay err=%v", err)
	}
}

func TestSessionSubjectHandoffRejectsLineageControllerDuplicateAndBearerMismatch(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	cwd := t.TempDir()
	const source, child = "logical-session-source", "logical-session-child"
	turnRef := "refs/sessions/" + source + "/turns/1"
	if err := service.ConfigureSessionLineageVerifier(SessionLineageVerifierFunc(func(_ context.Context, check SessionLineageCheck) error {
		if check.ChildSubject == "wrong-lineage" {
			return errors.New("not a direct child")
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	handle, credential := createDurableScope(t, service, cwd, source)
	other, _ := createDurableScope(t, service, cwd, "other-logical-session")
	if _, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, other.controllerToken, child, turnRef); !errors.Is(err, ErrSessionController) {
		t.Fatalf("wrong controller reserve err=%v", err)
	}
	if _, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, "wrong-lineage", turnRef); err == nil {
		t.Fatal("lineage verifier rejection was ignored")
	}
	if _, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, "other-logical-session", turnRef); !errors.Is(err, ErrSessionScopeExists) {
		t.Fatalf("active duplicate child err=%v", err)
	}
	if _, _, err := service.ReserveSessionScope(handle.SessionID, handle.controllerToken, "initially-reserved-child", cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, "initially-reserved-child", turnRef); !errors.Is(err, ErrSessionScopeActive) {
		t.Fatalf("handoff overlapped initial scope reservation: %v", err)
	}
	reservation, err := service.ReserveSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, child, turnRef)
	if err != nil {
		t.Fatal(err)
	}
	mismatched := handoffCredential(credential, child)
	mismatched.ResumeSecret = "resume_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := service.CommitSessionSubjectHandoff(context.Background(), handle.SessionID, handle.controllerToken, reservation.ID, mismatched); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("mismatched stable bearer err=%v", err)
	}
	if _, _, err := service.ReserveSessionScope(handle.SessionID, handle.controllerToken, child, cwd); !errors.Is(err, ErrSessionScopeActive) {
		t.Fatalf("initial scope reservation overlapped handoff: %v", err)
	}
}

func TestSessionSubjectHandoffReplayRejectsOrdinarySubjectMutation(t *testing.T) {
	service, store := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	handle, _ := createDurableScope(t, service, t.TempDir(), "logical-session-source")
	var previous sessionScopeSnapshot
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store == sessionScopeWALStore && event.Session == handle.SessionID {
				if err := json.Unmarshal(event.Data, &previous); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	forged := previous
	forged.Subject = "logical-session-child"
	forged.Version++
	forged.UpdatedAt = forged.UpdatedAt.Add(time.Second)
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(wal.Transaction{
		ID: "forged-subject", IdempotencyKey: "forged-subject", Principal: "broker", Actor: "broker:session-scope",
		Events: []wal.Event{{Store: sessionScopeWALStore, Type: string(sessionScopeAttached), Session: handle.SessionID, Data: data}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreSessionScopeSnapshots(store); err == nil || !strings.Contains(err.Error(), "without explicit handoff") {
		t.Fatalf("ordinary subject mutation replay err=%v", err)
	}
}

func TestSessionSubjectHandoffReplayRequiresPriorExactReservation(t *testing.T) {
	service, store := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	handle, _ := createDurableScope(t, service, t.TempDir(), "logical-session-source")
	var previous sessionScopeSnapshot
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store == sessionScopeWALStore && event.Session == handle.SessionID {
				if err := json.Unmarshal(event.Data, &previous); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	now := previous.UpdatedAt.Add(time.Second)
	const handoffID = "future-reservation"
	const child = "logical-session-child"
	turnRef := "refs/sessions/" + previous.Subject + "/turns/1"
	forged := previous
	forged.Subject = child
	forged.Version++
	forged.ControllerVersion++
	controller := sha256.Sum256([]byte("future-controller"))
	forged.ControllerDigest = hex.EncodeToString(controller[:])
	forged.UpdatedAt = now.Add(time.Second)
	forged.SubjectHandoff = &sessionSubjectHandoffCommit{
		ID: handoffID, SourceSubject: previous.Subject, ChildSubject: child, SourceTurnRef: turnRef,
	}
	forgedData, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(wal.Transaction{
		ID: "future-handoff-commit", IdempotencyKey: "future-handoff-commit", Principal: "broker", Actor: "broker:session-scope",
		Events: []wal.Event{{Store: sessionScopeWALStore, Type: string(sessionScopeAttached), Session: handle.SessionID, Data: forgedData}},
	}); err != nil {
		t.Fatal(err)
	}
	reservation := sessionSubjectHandoffReservation{
		Schema: sessionScopeSchema, ID: handoffID, SessionID: handle.SessionID,
		Generation: previous.Generation, ControllerVersion: previous.ControllerVersion,
		Principal: previous.Principal, RepoID: previous.RepoID, SourceCWD: previous.Handle.CWD,
		SourceSubject: previous.Subject, ChildSubject: child, SourceTurnRef: turnRef,
		TicketHash: previous.TicketDigest, ResumeHash: previous.ResumeDigest,
		ControllerHash: previous.ControllerDigest,
		CreatedAt:      now, ExpiresAt: now.Add(time.Minute),
	}
	reservationData, err := json.Marshal(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(wal.Transaction{
		ID: "future-handoff-reserve", IdempotencyKey: "future-handoff-reserve", Principal: "broker", Actor: "broker:session-scope",
		Events: []wal.Event{{Store: sessionHandoffWALStore, Type: "reserved", Session: handle.SessionID, Data: reservationData}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreSessionScopeSnapshots(store); err == nil || !strings.Contains(err.Error(), "not durably prior") {
		t.Fatalf("future reservation replay err=%v", err)
	}
}

func TestSessionSubjectHandoffRPCIsControllerBoundAndStrict(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	const source, child = "logical-session-source", "logical-session-child"
	turnRef := "refs/sessions/" + source + "/turns/4"
	configureExactHandoffLineage(t, service, source, child, turnRef)
	handle, credential := createDurableScope(t, service, t.TempDir(), source)

	raw := dispatchRPC(t, service, MethodSessionHandoffReserve, SessionHandoffReserveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		ChildSubject: child, SourceTurnRef: turnRef,
	})
	var reservation SessionSubjectHandoffReservation
	if err := json.Unmarshal(raw, &reservation); err != nil || reservation.ID == "" {
		t.Fatalf("reserve result=%s err=%v", raw, err)
	}
	childCredential := handoffCredential(credential, child)
	committedRaw := dispatchRPC(t, service, MethodSessionHandoffCommit, SessionHandoffCommitParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken, HandoffID: reservation.ID,
		ChildSubject: child, Ticket: childCredential.Ticket, ResumeSecret: childCredential.ResumeSecret,
	})
	var committed SessionHandleResult
	if err := json.Unmarshal(committedRaw, &committed); err != nil || committed.Subject != child ||
		committed.SessionID != handle.SessionID || committed.ControllerToken == handle.controllerToken {
		t.Fatalf("commit result=%s err=%v", committedRaw, err)
	}

	extra := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"controller_token":%q,"child_subject":"another-child","source_turn_ref":%q,"generation":99}`,
		handle.SessionID, committed.ControllerToken, "refs/sessions/"+child+"/turns/1"))
	if _, err := service.Dispatch(context.Background(), MethodSessionHandoffReserve, extra); err == nil {
		t.Fatal("handoff reserve accepted guest-selected generation")
	}
	wrongController, _ := json.Marshal(SessionHandoffReserveParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		ChildSubject: "another-child", SourceTurnRef: "refs/sessions/" + child + "/turns/1",
	})
	if _, err := service.Dispatch(context.Background(), MethodSessionHandoffReserve, wrongController); err == nil {
		t.Fatal("handoff reserve accepted rotated source controller")
	}
}
