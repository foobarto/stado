package wal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	logName        = "transactions.jsonl"
	lockName       = "broker.lock"
	epochName      = "epoch"
	quarantineName = "recovered-tail.bin"
	maxRecordBytes = 8 << 20
)

// Store owns the exclusive writer lock for its lifetime.
type Store struct {
	mu         sync.Mutex
	dir        string
	lock       *os.File
	log        *os.File
	epoch      uint64
	records    []Record
	byKey      map[string]int
	closed     bool
	now        func() time.Time
	tailInfo   error
	sharedKey  string
	sharedRefs int
}

var sharedStores = struct {
	sync.Mutex
	byDir map[string]*Store
}{byDir: map[string]*Store{}}

// OpenShared acquires the process's broker-owned handle. Calls for the same
// canonical root share one OS lock and are reference counted; a different
// process or an ordinary Open still fails closed on the lifetime lock.
func OpenShared(dir string) (*Store, error) {
	clean, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	clean = filepath.Clean(clean)
	sharedStores.Lock()
	defer sharedStores.Unlock()
	if current := sharedStores.byDir[clean]; current != nil && !current.closed {
		current.sharedRefs++
		return current, nil
	}
	s, err := Open(clean)
	if err != nil {
		return nil, err
	}
	s.sharedKey, s.sharedRefs = clean, 1
	sharedStores.byDir[clean] = s
	return s, nil
}

// Open creates or verifies a private WAL root, acquires its lifetime lock,
// increments the durable broker epoch, and recovers an incomplete final write.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("broker wal: directory required")
	}
	clean, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlinkPath(clean); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, fmt.Errorf("broker wal: create root: %w", err)
	}
	if err := rejectSymlinkPath(clean); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(filepath.Join(clean, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(lf); err != nil {
		_ = lf.Close()
		return nil, err
	}
	s := &Store{dir: clean, lock: lf, byKey: map[string]int{}, now: time.Now}
	if err := s.openLocked(); err != nil {
		_ = unlockFile(lf)
		_ = lf.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) openLocked() error {
	epoch, err := incrementEpoch(s.dir)
	if err != nil {
		return err
	}
	s.epoch = epoch
	path := filepath.Join(s.dir, logName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	s.log = f
	if err := s.loadAndRecover(); err != nil {
		_ = f.Close()
		return err
	}
	_, err = f.Seek(0, io.SeekEnd)
	return err
}

// Append durably commits tx or returns its existing record for an idempotent retry.
func (s *Store) Append(tx Transaction) (AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AppendResult{}, ErrClosed
	}
	if err := validateTransaction(tx); err != nil {
		return AppendResult{}, err
	}
	if i, ok := s.byKey[tx.IdempotencyKey]; ok {
		existing := s.records[i]
		if !sameTransaction(existing.Transaction, tx) {
			return AppendResult{}, ErrConflict
		}
		return AppendResult{Record: existing, Previously: true}, nil
	}
	prev := ""
	if len(s.records) > 0 {
		prev = s.records[len(s.records)-1].Digest
	}
	rec := Record{
		Schema: SchemaVersion, Sequence: uint64(len(s.records) + 1), Epoch: s.epoch,
		Timestamp: s.now().UTC().Format(time.RFC3339Nano), PreviousDigest: prev,
		Transaction: tx,
	}
	digest, err := recordDigest(rec)
	if err != nil {
		return AppendResult{}, err
	}
	rec.Digest = digest
	data, err := json.Marshal(rec)
	if err != nil {
		return AppendResult{}, err
	}
	if len(data) > maxRecordBytes {
		return AppendResult{}, fmt.Errorf("broker wal: record exceeds %d bytes", maxRecordBytes)
	}
	data = append(data, '\n')
	if _, err := s.log.Write(data); err != nil {
		return AppendResult{}, err
	}
	if err := s.log.Sync(); err != nil {
		return AppendResult{}, err
	}
	s.byKey[tx.IdempotencyKey] = len(s.records)
	s.records = append(s.records, rec)
	return AppendResult{Record: rec}, nil
}

func (s *Store) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

func (s *Store) Epoch() uint64 { return s.epoch }

// RecoveryInfo returns ErrInvalidTail when Open quarantined an incomplete final record.
func (s *Store) RecoveryInfo() error { return s.tailInfo }

func (s *Store) Close() error {
	if s.sharedKey != "" {
		sharedStores.Lock()
		if s.sharedRefs > 1 {
			s.sharedRefs--
			sharedStores.Unlock()
			return nil
		}
		delete(sharedStores.byDir, s.sharedKey)
		s.sharedRefs = 0
		sharedStores.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if s.log != nil {
		errs = append(errs, s.log.Close())
	}
	if s.lock != nil {
		errs = append(errs, unlockFile(s.lock), s.lock.Close())
	}
	return errors.Join(errs...)
}

func (s *Store) loadAndRecover() error {
	data, err := io.ReadAll(s.log)
	if err != nil {
		return err
	}
	validEnd := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), maxRecordBytes+1)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := append([]byte(nil), scanner.Bytes()...)
		validEnd += len(line) + 1
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			if validEnd >= len(data) {
				return s.recoverTail(data, validEnd-len(line)-1)
			}
			return fmt.Errorf("%w: record %d: %v", ErrCorrupt, lineNo, err)
		}
		if err := s.verifyAndAdd(rec); err != nil {
			if validEnd >= len(data) {
				return s.recoverTail(data, validEnd-len(line)-1)
			}
			return fmt.Errorf("%w: record %d: %v", ErrCorrupt, lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: scan: %v", ErrCorrupt, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := bytes.LastIndexByte(data, '\n')
		return s.recoverTail(data, lastNL+1)
	}
	return nil
}

func (s *Store) verifyAndAdd(rec Record) error {
	if rec.Schema != SchemaVersion || rec.Sequence != uint64(len(s.records)+1) {
		return errors.New("schema or sequence mismatch")
	}
	prev := ""
	if len(s.records) > 0 {
		prev = s.records[len(s.records)-1].Digest
	}
	if rec.PreviousDigest != prev {
		return errors.New("chain mismatch")
	}
	want, err := recordDigest(rec)
	if err != nil || rec.Digest != want {
		return errors.New("digest mismatch")
	}
	if _, exists := s.byKey[rec.Transaction.IdempotencyKey]; exists {
		return errors.New("duplicate idempotency key")
	}
	s.byKey[rec.Transaction.IdempotencyKey] = len(s.records)
	s.records = append(s.records, rec)
	return nil
}

func (s *Store) recoverTail(data []byte, offset int) error {
	if offset < 0 || offset > len(data) {
		return ErrCorrupt
	}
	tail := data[offset:]
	if len(tail) > 0 {
		if err := os.WriteFile(filepath.Join(s.dir, quarantineName), tail, 0o600); err != nil {
			return err
		}
	}
	if err := s.log.Truncate(int64(offset)); err != nil {
		return err
	}
	if _, err := s.log.Seek(int64(offset), io.SeekStart); err != nil {
		return err
	}
	if err := s.log.Sync(); err != nil {
		return err
	}
	s.tailInfo = ErrInvalidTail
	return nil
}

func validateTransaction(tx Transaction) error {
	if tx.ID == "" || tx.IdempotencyKey == "" || tx.Principal == "" || tx.Actor == "" {
		return errors.New("broker wal: transaction id, idempotency key, principal, and actor are required")
	}
	if len(tx.Events) == 0 {
		return errors.New("broker wal: at least one event required")
	}
	for _, ev := range tx.Events {
		if ev.Store == "" || ev.Type == "" || (len(ev.Data) > 0 && !json.Valid(ev.Data)) {
			return errors.New("broker wal: event store/type and valid JSON data required")
		}
	}
	return nil
}

func sameTransaction(a, b Transaction) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}

func recordDigest(rec Record) (string, error) {
	rec.Digest = ""
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func incrementEpoch(dir string) (uint64, error) {
	path := filepath.Join(dir, epochName)
	var epoch uint64
	if b, err := os.ReadFile(path); err == nil {
		if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &epoch); err != nil {
			return 0, fmt.Errorf("broker wal: invalid epoch: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	epoch++
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d\n", epoch)), 0o600); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	if err := syncDir(dir); err != nil {
		return 0, err
	}
	return epoch, nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func rejectSymlinkPath(path string) error {
	cur := filepath.Clean(path)
	for {
		info, err := os.Lstat(cur)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("broker wal: symlinked path rejected: %s", cur)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}
