package brokercredential

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/broker"
)

func testCredential(subject string) broker.SessionAdoptionCredential {
	return broker.SessionAdoptionCredential{
		Subject:      subject,
		Ticket:       "scope_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ResumeSecret: "resume_fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
	}
}

func TestStoreRoundTripPrivateNoFollow(t *testing.T) {
	stateDir := t.TempDir()
	store, err := New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	want := testCredential("logical-session-a")
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(store.root)
	if err != nil || rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential root mode=%v err=%v", rootInfo.Mode(), err)
	}
	path := filepath.Join(store.root, fileName(want.Subject))
	fileInfo, err := os.Lstat(path)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode=%v err=%v", fileInfo.Mode(), err)
	}
	got, err := store.Load(want.Subject)
	if err != nil || got != want {
		t.Fatalf("Load=%+v err=%v want=%+v", got, err, want)
	}
	replacement := want
	replacement.Ticket = "scope_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	replacement.ResumeSecret = "resume_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := store.Save(replacement); err != nil {
		t.Fatalf("atomically replace credential: %v", err)
	}
	if got, err := store.Load(want.Subject); err != nil || got != replacement {
		t.Fatalf("Load replacement=%+v err=%v want=%+v", got, err, replacement)
	}
	if err := store.Remove(want.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(want.Subject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after remove err=%v", err)
	}
}

func TestStoreRejectsSymlinkedRootAndFile(t *testing.T) {
	stateDir := t.TempDir()
	store, _ := New(stateDir)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(store.root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, store.root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := store.Save(testCredential("logical-session-a")); err == nil {
		t.Fatal("Save followed symlinked credential root")
	}

	stateDir2 := t.TempDir()
	store2, _ := New(stateDir2)
	if err := os.MkdirAll(store2.root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "credential.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store2.root, fileName("logical-session-a"))); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.Load("logical-session-a"); err == nil {
		t.Fatal("Load followed symlinked credential file")
	}
	if err := store2.Save(testCredential("logical-session-a")); err == nil {
		t.Fatal("Save replaced symlinked credential file")
	}
}

func TestStoreRejectsModesAndSubjectMismatch(t *testing.T) {
	store, _ := New(t.TempDir())
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testCredential("logical-session-a")); err == nil {
		t.Fatal("Save accepted permissive credential directory")
	}

	store, _ = New(t.TempDir())
	if err := store.Save(testCredential("logical-session-a")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.root, fileName("logical-session-a"))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("logical-session-a"); err == nil {
		t.Fatal("Load accepted permissive credential file")
	}
	if _, err := store.Load("logical-session-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("different logical subject err=%v", err)
	}
}

func TestStoreRejectsLookalikeBearerPrefixes(t *testing.T) {
	store, _ := New(t.TempDir())
	credential := testCredential("logical-session-a")
	credential.Ticket = "forged" + credential.Ticket[len("scope_"):]
	if err := store.Save(credential); err == nil {
		t.Fatal("Save accepted a ticket with a lookalike prefix")
	}
	credential = testCredential("logical-session-a")
	credential.ResumeSecret = "forged_" + credential.ResumeSecret[len("resume_"):]
	if err := store.Save(credential); err == nil {
		t.Fatal("Save accepted a resume secret with a lookalike prefix")
	}
}

func TestStoreCanStageStableBearerForPendingChildSubject(t *testing.T) {
	store, _ := New(t.TempDir())
	source := testCredential("logical-session-a")
	if err := store.Save(source); err != nil {
		t.Fatal(err)
	}
	child, err := store.StageHandoff(source.Subject, "logical-session-compacted-child")
	if err != nil {
		t.Fatal(err)
	}
	gotSource, sourceErr := store.Load(source.Subject)
	gotChild, childErr := store.Load(child.Subject)
	if sourceErr != nil || childErr != nil || gotSource != source || gotChild != child {
		t.Fatalf("staged source=%+v/%v child=%+v/%v", gotSource, sourceErr, gotChild, childErr)
	}
	if child.Ticket != source.Ticket || child.ResumeSecret != source.ResumeSecret {
		t.Fatal("handoff staging changed the stable recovery bearer")
	}
	if replayed, err := store.StageHandoff(source.Subject, child.Subject); err != nil || replayed != child {
		t.Fatalf("idempotent stage=%+v err=%v", replayed, err)
	}
	different := testCredential(child.Subject)
	different.Ticket = "scope_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	different.ResumeSecret = "resume_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := store.Save(different); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageHandoff(source.Subject, child.Subject); err == nil {
		t.Fatal("handoff staging overwrote a different child bearer")
	}
}
