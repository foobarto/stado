package wal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testTx(id, key string) Transaction {
	return Transaction{
		ID: id, IdempotencyKey: key, Principal: "user:test", Actor: "test",
		Events: []Event{{Store: "artifact", Type: "create", Data: json.RawMessage(`{"id":"a"}`)}},
	}
}

func TestStoreAppendReopenAndChain(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(testTx("t1", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Append(testTx("t2", "k2"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Record.Sequence != 1 || second.Record.Sequence != 2 || second.Record.PreviousDigest != first.Record.Digest {
		t.Fatalf("bad chain: %#v %#v", first.Record, second.Record)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Epoch() != 2 || len(s.Records()) != 2 {
		t.Fatalf("epoch/records = %d/%d", s.Epoch(), len(s.Records()))
	}
}

func TestStoreIdempotency(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx := testTx("t1", "same")
	if _, err := s.Append(tx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Append(tx)
	if err != nil || !got.Previously || len(s.Records()) != 1 {
		t.Fatalf("retry = %+v err=%v records=%d", got, err, len(s.Records()))
	}
	changed := testTx("different", "same")
	if _, err := s.Append(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err = %v", err)
	}
}

func TestStoreExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open err = %v", err)
	}
}

func TestOpenSharedReferenceCountsOneProcessWriter(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenShared(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenShared(filepath.Join(dir, "."))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Epoch() != second.Epoch() {
		t.Fatal("shared opens did not return one broker-owned store")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Append(testTx("shared", "shared")); err != nil {
		t.Fatalf("remaining reference was closed: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := OpenShared(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if third.Epoch() <= first.Epoch() {
		t.Fatalf("fresh ownership did not advance epoch: %d", third.Epoch())
	}
}

func TestStoreRecoversInvalidFinalRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(testTx("t1", "k1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, logName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"broken":`)
	_ = f.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !errors.Is(s.RecoveryInfo(), ErrInvalidTail) || len(s.Records()) != 1 {
		t.Fatalf("recovery=%v records=%d", s.RecoveryInfo(), len(s.Records()))
	}
	if _, err := os.Stat(filepath.Join(dir, quarantineName)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(testTx("t2", "k2")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsInteriorCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append(testTx("t1", "k1"))
	_, _ = s.Append(testTx("t2", "k2"))
	_ = s.Close()
	p := filepath.Join(dir, logName)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		if b[i] == 'a' {
			b[i] = 'z'
			break
		}
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt open err = %v", err)
	}
}

func TestStoreRejectsSymlinkRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
