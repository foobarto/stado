package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

func scopeVerifier() ArtifactPluginVerifier {
	return ArtifactPluginVerifierFunc(func(_ context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		return identity, manifest, nil
	})
}

func openScopeService(t *testing.T, dir string) (*Service, *wal.Store) {
	t.Helper()
	store, err := wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(DefaultPolicy(), nil)
	if err := service.ConfigureArtifactStore(store, scopeVerifier()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return service, store
}

func createDurableScope(t *testing.T, service *Service, cwd, subject string) (SessionHandle, SessionAdoptionCredential) {
	t.Helper()
	handle, decision, err := service.CreateSessionForSubject(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd,
	}, subject)
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSessionForSubject handle=%+v decision=%+v err=%v", handle, decision, err)
	}
	credential := SessionAdoptionCredential{
		Subject: handle.subject, Ticket: handle.adoptionTicket, ResumeSecret: handle.resumeSecret,
	}
	if credential.Subject != subject || credential.Ticket == "" || credential.ResumeSecret == "" || handle.controllerToken == "" {
		t.Fatalf("incomplete durable handle/credential: handle=%+v credential=%+v", handle, credential)
	}
	service.sessionsMu.RLock()
	state := service.sessions[handle.SessionID]
	if state.scope.ticket != "" || state.scope.resumeSecret != "" {
		service.sessionsMu.RUnlock()
		t.Fatal("broker retained plaintext adoption credentials")
	}
	service.sessionsMu.RUnlock()
	return handle, credential
}

func TestDurableSessionScopeRestartPreservesWholeApplicationScope(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	cwd := t.TempDir()
	first, firstStore := openScopeService(t, walDir)
	handle, credential := createDurableScope(t, first, cwd, "logical-session-a")

	auth := application.Authority{
		SessionID: handle.SessionID, Generation: 1, PluginID: "example.test/watcher",
		Principal: "os-user:test", Actor: "plugin:example.test/watcher@v1",
	}
	entry, err := first.artifacts.application.AppendJournal(context.Background(), auth, application.JournalAppend{
		ID: "checkpoint", RunID: "run-1", Kind: "checkpoint", Summary: "durable",
		Data: json.RawMessage(`{"turn":7}`),
	}, "journal-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	if entry.WALSequence == 0 {
		t.Fatal("journal entry lacks WAL sequence")
	}
	run, err := first.artifacts.application.RequestWorkerRun(context.Background(), auth, application.WorkerRunRequest{
		RunID: "run-1", Objective: "finish the task", Prompt: "continue", Conflict: application.WorkerRunRejectOperatorLoop,
	}, "worker-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	run, err = first.artifacts.application.ActivateWorkerRun(context.Background(), auth, application.WorkerRunCAS{
		RunID: run.RunID, ExpectedVersion: run.Version,
	}, "activate-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.artifacts.application.AcquireHold(context.Background(), auth, application.HoldAcquire{
		ID: "hold-1", RunID: run.RunID, ReasonCode: "review", TTL: time.Hour,
	}, "hold-before-restart"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.artifacts.application.ScheduleTimer(context.Background(), auth, application.TimerSchedule{
		ID: "timer-1", RunID: run.RunID, Name: "review", DueAt: time.Now().UTC().Add(time.Hour),
		Payload: json.RawMessage(`{"phase":"review"}`),
	}, "timer-before-restart"); err != nil {
		t.Fatal(err)
	}
	event, err := first.artifacts.application.PublishEvent(context.Background(), application.SessionScope{
		SessionID: handle.SessionID, Generation: 1,
	}, application.EventInput{ID: "turn-7", Kind: "host.turn", Data: json.RawMessage(`{"turn":7}`)}, "event-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.artifacts.application.AcknowledgeEvent(context.Background(), auth, application.EventAck{Sequence: event.WALSequence}, "cursor-before-restart"); err != nil {
		t.Fatal(err)
	}
	operatorInput, err := first.artifacts.application.CaptureOperatorInput(context.Background(), application.SessionScope{
		SessionID: handle.SessionID, Generation: 1,
	}, "also inspect the release notes", "input-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.artifacts.application.RouteOperatorInput(context.Background(), auth, application.OperatorInputRoute{
		InputID: operatorInput.ID, RunID: run.RunID, ExpectedVersion: operatorInput.Version,
		Disposition: application.OperatorInputDefer, Label: "release notes",
	}, "route-before-restart"); err != nil {
		t.Fatal(err)
	}
	narrowed := handle.Effective
	narrowed.FSWrite = []string{}
	if err := first.NarrowEffective(handle.SessionID, narrowed); err != nil {
		t.Fatal(err)
	}
	if err := first.SetTaint(handle.SessionID, handle.controllerToken, TaintTainted); err != nil {
		t.Fatal(err)
	}

	records, err := json.Marshal(firstStore.Records())
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{
		"controller": handle.controllerToken, "ticket": credential.Ticket, "resume secret": credential.ResumeSecret,
	} {
		if strings.Contains(string(records), secret) {
			t.Fatalf("WAL contains plaintext %s", name)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _ := openScopeService(t, walDir)
	defer second.Close()
	if err := second.authenticateSessionController(handle.SessionID, handle.controllerToken); !errors.Is(err, ErrSessionController) {
		t.Fatalf("pre-restart controller survived broker epoch change: %v", err)
	}
	adopted, rotated, err := second.AdoptSession(credential, cwd)
	if err != nil {
		t.Fatalf("AdoptSession after broker restart: %v", err)
	}
	if adopted.SessionID != handle.SessionID || adopted.subject != credential.Subject {
		t.Fatalf("adopted scope changed identity: before=%+v after=%+v", handle, adopted)
	}
	if adopted.controllerToken == handle.controllerToken {
		t.Fatal("adoption did not rotate the live controller")
	}
	if rotated != credential {
		t.Fatal("one-phase adoption changed the durable recovery bearer")
	}
	projection, err := second.artifacts.application.Project(context.Background(), auth, application.ProjectionOptions{
		JournalLimit: 8, WorkerLimit: 8, DeferredTaskLimit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Journal) != 1 || projection.Journal[0].ID != "checkpoint" || projection.Journal[0].WALSequence != entry.WALSequence {
		t.Fatalf("application projection after restart = %+v", projection.Journal)
	}
	if len(projection.Holds) != 1 || len(projection.Timers) != 1 || len(projection.WorkerRuns) != 1 ||
		projection.WorkerRuns[0].Status != application.WorkerRunActive || len(projection.DeferredTasks) != 1 {
		t.Fatalf("incomplete application projection after restart: %+v", projection)
	}
	enforcement, err := second.artifacts.application.ProjectEnforcement(context.Background(), application.SessionScope{
		SessionID: handle.SessionID, Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(enforcement.ActiveHolds) != 1 || enforcement.ActiveWorkerRun == nil || enforcement.ActiveWorkerRun.RunID != run.RunID {
		t.Fatalf("incomplete enforcement projection after restart: %+v", enforcement)
	}
	pending, cursor, err := second.artifacts.application.PendingEvents(context.Background(), auth, []string{"host.turn", application.OperatorInputQueuedEvent}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 || cursor.Sequence != event.WALSequence {
		t.Fatalf("event cursor after restart pending=%+v cursor=%+v", pending, cursor)
	}
	storedHandle, terminated, err := second.LookupSession(handle.SessionID)
	if err != nil || terminated || len(storedHandle.Effective.FSWrite) != 0 {
		t.Fatalf("restored handle=%+v terminated=%v err=%v", storedHandle, terminated, err)
	}
	if taint, err := second.Taint(handle.SessionID); err != nil || taint != TaintTainted {
		t.Fatalf("restored taint=%v err=%v", taint, err)
	}
	if err := second.authenticateSessionController(handle.SessionID, handle.controllerToken); !errors.Is(err, ErrSessionController) {
		t.Fatalf("old controller remains valid: %v", err)
	}
	if _, _, err := second.AdoptSession(credential, cwd); !errors.Is(err, ErrSessionScopeActive) {
		t.Fatalf("stable recovery bearer bypassed exclusive live ownership: %v", err)
	}
}

func TestDurableSessionScopeExactSubjectExclusiveAdoption(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	handle, credential := createDurableScope(t, service, t.TempDir(), "logical-session-a")

	if _, _, err := service.CreateSessionForSubject(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: handle.CWD,
	}, credential.Subject); !errors.Is(err, ErrSessionScopeExists) {
		t.Fatalf("duplicate logical subject err = %v", err)
	}
	wrongSubject := credential
	wrongSubject.Subject = "logical-session-b"
	if _, _, err := service.AdoptSession(wrongSubject, handle.CWD); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("cross-conversation adoption err = %v", err)
	}
	if _, _, err := service.AdoptSession(credential, handle.CWD); !errors.Is(err, ErrSessionScopeActive) {
		t.Fatalf("active-owner adoption err = %v", err)
	}

	service.artifacts.mu.Lock()
	service.artifacts.bindings["old-binding"] = artifactBinding{
		token: "old-binding", sessionID: handle.SessionID, generation: 1, controllerVersion: 1,
	}
	service.artifacts.mu.Unlock()
	now = now.Add(sessionScopeLeaseDefault + time.Second)
	adopted, rotated, err := service.AdoptSession(credential, handle.CWD)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.SessionID != handle.SessionID || rotated.Subject != credential.Subject {
		t.Fatalf("adopted=%+v rotated=%+v", adopted, rotated)
	}
	if _, err := service.artifactBinding("old-binding"); err == nil {
		t.Fatal("old opaque plugin binding survived controller rotation")
	}
	if err := service.authenticateSessionController(handle.SessionID, handle.controllerToken); !errors.Is(err, ErrSessionController) {
		t.Fatalf("old controller auth err = %v", err)
	}
}

func TestDurableSessionScopeRepositoryBindingUsesCanonicalRoot(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstSubdir := filepath.Join(repo, "one")
	secondSubdir := filepath.Join(repo, "two")
	if err := os.MkdirAll(firstSubdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondSubdir, 0o700); err != nil {
		t.Fatal(err)
	}
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	handle, credential := createDurableScope(t, service, firstSubdir, "logical-session-a")
	if err := service.DetachSession(handle.SessionID, handle.controllerToken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AdoptSession(credential, secondSubdir); err != nil {
		t.Fatalf("same repository adoption from another subdirectory: %v", err)
	}
}

func TestDurableSessionScopeDetachAndTerminate(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	service, _ := openScopeService(t, walDir)
	handle, credential := createDurableScope(t, service, t.TempDir(), "logical-session-a")
	if err := service.DetachSession(handle.SessionID, handle.controllerToken); err != nil {
		t.Fatal(err)
	}
	if err := service.authenticateSessionController(handle.SessionID, handle.controllerToken); !errors.Is(err, ErrSessionController) {
		t.Fatalf("detached controller auth err = %v", err)
	}
	adopted, rotated, err := service.AdoptSession(credential, handle.CWD)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TerminateSession(adopted.SessionID, adopted.controllerToken); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, _ := openScopeService(t, walDir)
	defer restarted.Close()
	if _, _, err := restarted.AdoptSession(rotated, handle.CWD); !errors.Is(err, ErrSessionTerminated) {
		t.Fatalf("terminated scope adoption err = %v", err)
	}
}

func TestDurableSessionScopeConcurrentAdoptionHasOneOwner(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	handle, credential := createDurableScope(t, service, t.TempDir(), "logical-session-a")
	now = now.Add(sessionScopeLeaseDefault + time.Second)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := service.AdoptSession(credential, handle.CWD)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrSessionScopeCredential) && !errors.Is(err, ErrSessionScopeActive) {
			t.Fatalf("unexpected losing adopter error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful adopters = %d, want 1", successes)
	}
}

func TestDurableSessionScopeTwoPhaseReservationClosesCreateResponseLoss(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	cwd := t.TempDir()
	root, decision, err := service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd,
	})
	if err != nil || !decision.Admit {
		t.Fatalf("root create decision=%+v err=%v", decision, err)
	}
	credential, _, err := service.ReserveSessionScope(root.SessionID, root.controllerToken, "logical-session-a", cwd)
	if err != nil {
		t.Fatal(err)
	}
	created, decision, err := service.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: root.SessionID,
	}, credential)
	if err != nil || !decision.Admit {
		t.Fatalf("commit reservation decision=%+v err=%v", decision, err)
	}
	// Simulate losing the create reply: the client has only its prewritten
	// recovery bearer, not the returned session ID/controller.
	now = now.Add(sessionScopeLeaseDefault + time.Second)
	adopted, _, err := service.AdoptSession(credential, cwd)
	if err != nil {
		t.Fatalf("adopt after lost create response: %v", err)
	}
	if adopted.SessionID != created.SessionID || adopted.controllerToken == created.controllerToken {
		t.Fatalf("lost-response adoption created=%+v adopted=%+v", created, adopted)
	}
}

func TestDurableSessionScopeLostReservationExpiresWithoutApplicationState(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	cwd := t.TempDir()
	root, _, err := service.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	lost, _, err := service.ReserveSessionScope(root.SessionID, root.controllerToken, "logical-session-a", cwd)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(sessionReservationTTL + time.Second)
	replacement, _, err := service.ReserveSessionScope(root.SessionID, root.controllerToken, "logical-session-a", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Ticket == lost.Ticket || replacement.ResumeSecret == lost.ResumeSecret {
		t.Fatal("expired reservation bearer was reused")
	}
	if _, _, err := service.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: root.SessionID,
	}, lost); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("expired reservation committed: %v", err)
	}
}

func TestDurableSessionScopeReservationSurvivesBrokerRestartBeforeCommit(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	cwd := t.TempDir()
	first, _ := openScopeService(t, walDir)
	root, _, err := first.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	credential, _, err := first.ReserveSessionScope(root.SessionID, root.controllerToken, "logical-session-a", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _ := openScopeService(t, walDir)
	defer second.Close()
	newRoot, _, err := second.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: newRoot.SessionID,
	}, credential); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("old-parent reservation committed under new parent: %v", err)
	}
	replacement, _, err := second.ReserveSessionScope(newRoot.SessionID, newRoot.controllerToken, credential.Subject, cwd)
	if err != nil {
		t.Fatalf("replace orphaned restart reservation: %v", err)
	}
	if replacement.Ticket == credential.Ticket || replacement.ResumeSecret == credential.ResumeSecret {
		t.Fatal("restart replacement reused old-parent recovery bearer")
	}
	created, decision, err := second.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: newRoot.SessionID,
	}, replacement)
	if err != nil || !decision.Admit || created.subject != credential.Subject {
		t.Fatalf("post-restart reservation commit created=%+v decision=%+v err=%v", created, decision, err)
	}
}

func TestDurableSessionScopeReservationCannotCrossParents(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	cwd := t.TempDir()
	first, _, err := service.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	credential, _, err := service.ReserveSessionScope(first.SessionID, first.controllerToken, "logical-session-a", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: second.SessionID,
	}, credential); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("reservation minted for parent %q committed under %q: %v", first.SessionID, second.SessionID, err)
	}
	if _, decision, err := service.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: first.SessionID,
	}, credential); err != nil || !decision.Admit {
		t.Fatalf("failed cross-parent attempt consumed reservation: decision=%+v err=%v", decision, err)
	}
}

func TestDurableSessionScopeReplayRejectsDuplicateSessionBearers(t *testing.T) {
	for _, tc := range []struct {
		name            string
		duplicateTicket bool
	}{
		{name: "ticket", duplicateTicket: true},
		{name: "resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, store := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
			defer service.Close()
			createDurableScope(t, service, t.TempDir(), "logical-session-a")

			var original sessionScopeSnapshot
			for _, record := range store.Records() {
				for _, event := range record.Transaction.Events {
					if event.Store == sessionScopeWALStore {
						if err := json.Unmarshal(event.Data, &original); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if original.SessionID == "" {
				t.Fatal("missing original session-scope snapshot")
			}
			duplicate := original
			duplicate.SessionID = "scope-replay-duplicate"
			duplicate.Handle.SessionID = duplicate.SessionID
			duplicate.Subject = "logical-session-b"
			if tc.duplicateTicket {
				uniqueResume := sha256.Sum256([]byte("unique-resume"))
				duplicate.ResumeDigest = hex.EncodeToString(uniqueResume[:])
			} else {
				uniqueTicket := sha256.Sum256([]byte("unique-ticket"))
				duplicate.TicketDigest = hex.EncodeToString(uniqueTicket[:])
			}
			data, err := json.Marshal(duplicate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(wal.Transaction{
				ID: "duplicate-session-" + tc.name, IdempotencyKey: "duplicate-session-" + tc.name,
				Principal: "broker", Actor: "broker:session-scope",
				Events: []wal.Event{{Store: sessionScopeWALStore, Type: string(sessionScopeAttached), Session: duplicate.SessionID, Data: data}},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := restoreSessionScopeSnapshots(store); err == nil || !strings.Contains(err.Error(), "share a recovery "+tc.name+" digest") {
				t.Fatalf("duplicate %s replay error = %v", tc.name, err)
			}
		})
	}
}

func TestDurableSessionScopeRejectsUnreservedAndMixedBearers(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	cwd := t.TempDir()
	root, _, err := service.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	unreserved := testCredentialForBroker("logical-session-a")
	if _, _, err := service.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: root.SessionID,
	}, unreserved); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("client-chosen unreserved bearer err=%v", err)
	}
	first, _, err := service.ReserveSessionScope(root.SessionID, root.controllerToken, "logical-session-a", cwd)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.ReserveSessionScope(root.SessionID, root.controllerToken, "logical-session-b", cwd)
	if err != nil {
		t.Fatal(err)
	}
	mixed := first
	mixed.ResumeSecret = second.ResumeSecret
	if _, _, err := service.CreateSessionForCredential(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, SessionID: root.SessionID,
	}, mixed); !errors.Is(err, ErrSessionScopeCredential) {
		t.Fatalf("mixed reserved bearer err=%v", err)
	}
}

func testCredentialForBroker(subject string) SessionAdoptionCredential {
	return SessionAdoptionCredential{
		Subject:      subject,
		Ticket:       "scope_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ResumeSecret: "resume_fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
	}
}

func TestDurableSessionScopeWALNeverContainsPlaintextBearers(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	service, _ := openScopeService(t, walDir)
	handle, credential := createDurableScope(t, service, t.TempDir(), "logical-session-a")
	if err := service.DetachSession(handle.SessionID, handle.controllerToken); err != nil {
		t.Fatal(err)
	}
	adopted, rotated, err := service.AdoptSession(credential, handle.CWD)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(walDir, "transactions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{
		"initial controller": handle.controllerToken,
		"initial ticket":     credential.Ticket, "initial resume": credential.ResumeSecret,
		"rotated controller": adopted.controllerToken,
		"rotated ticket":     rotated.Ticket, "rotated resume": rotated.ResumeSecret,
	} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("WAL contains plaintext %s", name)
		}
	}
}

func TestDurableSessionScopeRPCExactSubjectCursorAndController(t *testing.T) {
	service, _ := openScopeService(t, filepath.Join(t.TempDir(), "wal"))
	defer service.Close()
	cwd := t.TempDir()
	rootRaw := dispatchRPC(t, service, MethodSessionCreate, SessionCreateParams{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd,
	})
	var root SessionHandleResult
	if err := json.Unmarshal(rootRaw, &root); err != nil {
		t.Fatal(err)
	}
	reservedRaw := dispatchRPC(t, service, MethodSessionReserve, SessionReserveParams{
		ParentSessionID: root.SessionID, ParentControllerToken: root.ControllerToken,
		Subject: "logical-session-a", CWD: cwd,
	})
	var preStaged SessionReserveResult
	if err := json.Unmarshal(reservedRaw, &preStaged); err != nil {
		t.Fatal(err)
	}
	raw := dispatchRPC(t, service, MethodSessionCreate, SessionCreateParams{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: cwd, Subject: "logical-session-a",
		AdoptionTicket: preStaged.Ticket, ResumeSecret: preStaged.ResumeSecret,
		ParentSessionID: root.SessionID, ParentControllerToken: root.ControllerToken,
	})
	var created SessionHandleResult
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Subject != "logical-session-a" || created.AdoptionTicket == "" || created.ResumeSecret == "" {
		t.Fatalf("durable create result=%+v", created)
	}
	dispatchRPC(t, service, MethodSessionHeartbeat, SessionHeartbeatParams{
		SessionID: created.SessionID, ControllerToken: created.ControllerToken,
	})

	adoptRaw, _ := json.Marshal(SessionAdoptParams{
		Subject: created.Subject, Ticket: created.AdoptionTicket, ResumeSecret: created.ResumeSecret, CWD: cwd,
	})
	if _, err := service.Dispatch(context.Background(), MethodSessionAdopt, adoptRaw); err == nil {
		t.Fatal("active durable scope was adopted twice")
	} else if dispatchErr, ok := err.(*DispatchError); !ok || dispatchErr.Code != ErrCodeSessionScopeActive {
		t.Fatalf("active adoption error=%T %v", err, err)
	}

	dispatchRPC(t, service, MethodSessionDetach, SessionDetachParams{
		SessionID: created.SessionID, ControllerToken: created.ControllerToken,
	})
	wrongRepo := SessionAdoptParams{
		Subject: created.Subject, Ticket: created.AdoptionTicket, ResumeSecret: created.ResumeSecret, CWD: t.TempDir(),
	}
	wrongRaw, _ := json.Marshal(wrongRepo)
	if _, err := service.Dispatch(context.Background(), MethodSessionAdopt, wrongRaw); err == nil {
		t.Fatal("adoption crossed the broker-recorded repository")
	} else if dispatchErr, ok := err.(*DispatchError); !ok || dispatchErr.Code != ErrCodeSessionScopeCredential {
		t.Fatalf("cross-repository adoption error=%T %v", err, err)
	}

	adoptedRaw := dispatchRPC(t, service, MethodSessionAdopt, SessionAdoptParams{
		Subject: created.Subject, Ticket: created.AdoptionTicket, ResumeSecret: created.ResumeSecret, CWD: cwd,
	})
	var adopted SessionHandleResult
	if err := json.Unmarshal(adoptedRaw, &adopted); err != nil {
		t.Fatal(err)
	}
	if adopted.SessionID != created.SessionID || adopted.ControllerToken == created.ControllerToken ||
		adopted.AdoptionTicket != created.AdoptionTicket || adopted.ResumeSecret != created.ResumeSecret {
		t.Fatalf("adopted result=%+v created=%+v", adopted, created)
	}
	if err := service.authenticateSessionController(created.SessionID, created.ControllerToken); !errors.Is(err, ErrSessionController) {
		t.Fatalf("old controller survived RPC adoption: %v", err)
	}

	// Strict decoding prevents a native caller from smuggling a broker session
	// or generation selector into the adoption credential payload.
	malformed := json.RawMessage(`{"subject":"logical-session-a","ticket":"x","resume_secret":"y","cwd":"z","session_id":"guest-picked"}`)
	if _, err := service.Dispatch(context.Background(), MethodSessionAdopt, malformed); err == nil {
		t.Fatal("adoption accepted guest-selected session_id")
	}
}
