package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/foobarto/stado/internal/artifacts"
)

const (
	legacyMemorySourcePath = "memory/memory.jsonl"
	legacyMemoryArchiveDir = "memory/archive"
	legacyMemoryArchive    = "memory/archive/memory.jsonl"
	maxLegacyMemoryBytes   = 128 << 20
)

// legacyMemorySource owns the sole historical filesystem bridge. Every path is
// a fixed relative name under the daemon-configured state root; the WASM guest
// supplies neither paths nor bytes.
type legacyMemorySource struct {
	mu   sync.Mutex
	root *os.Root
}

// ConfigureLegacyMemoryMigration opens the trusted daemon state root once.
// Migration is unavailable unless this explicit broker-owned source exists.
func (s *Service) ConfigureLegacyMemoryMigration(stateDir string) error {
	if s == nil || s.artifacts == nil || s.artifacts.service == nil {
		return errors.New("broker: artifact authority must be configured before legacy migration")
	}
	if strings.TrimSpace(stateDir) == "" || !filepath.IsAbs(stateDir) {
		return errors.New("broker: legacy migration state root must be absolute")
	}
	root, err := os.OpenRoot(filepath.Clean(stateDir))
	if err != nil {
		return err
	}
	s.artifacts.mu.Lock()
	defer s.artifacts.mu.Unlock()
	if s.artifacts.legacyMemory != nil {
		_ = root.Close()
		return errors.New("broker: legacy migration source already configured")
	}
	s.artifacts.legacyMemory = &legacyMemorySource{root: root}
	return nil
}

func (s *Service) migrateLegacyMemory(ctx context.Context, binding artifactBinding) (artifacts.MigrationResult, error) {
	state := s.artifacts
	if state == nil || state.service == nil || state.kinds == nil || state.legacyMemory == nil {
		return artifacts.MigrationResult{}, errors.New("legacy memory migration authority unavailable")
	}
	if !binding.lifecycle || !binding.hasCapability("artifact:migrate:legacy-memory-v1") || !binding.caps.MigrateLegacyMemoryV1 {
		return artifacts.MigrationResult{}, errors.New("legacy memory migration capability is not admitted")
	}
	identity := binding.legacyMigration
	if identity.Runtime != binding.identity {
		return artifacts.MigrationResult{}, errors.New("legacy migration identity is not bound to this lifecycle application")
	}
	// Re-resolve both exact descriptors from the broker registry on every call.
	// This independently fences manifest identity and both schema digests even
	// if a stale in-memory binding survived an attempted plugin replacement.
	memory, memoryOK := state.kinds.Lookup(identity.Memory.Kind)
	lesson, lessonOK := state.kinds.Lookup(identity.Lesson.Kind)
	if !memoryOK || !lessonOK || !sameMigrationDescriptor(memory, identity.Memory) || !sameMigrationDescriptor(lesson, identity.Lesson) {
		return artifacts.MigrationResult{}, errors.New("legacy migration signed descriptors changed after admission")
	}
	if prior, found, err := state.service.CompletedLegacyMigration(identity); found || err != nil {
		return prior, err
	}
	state.legacyMemory.mu.Lock()
	defer state.legacyMemory.mu.Unlock()
	raw, archiveDigest, err := state.legacyMemory.readArchiveAndRecheckLocked()
	if err != nil {
		return artifacts.MigrationResult{}, err
	}
	resolve := func(subject string) (string, bool) {
		return s.resolveLegacySessionAnchor(binding.principal, subject)
	}
	return state.service.MigrateLegacy(ctx, artifacts.LegacyMigration{
		RawLog: raw, ArchiveDigest: archiveDigest,
		Principal: binding.principal, Actor: binding.actor(), Identity: identity,
		ResolveSessionAnchor: resolve,
		ValidateSource: func() error {
			// This is the narrowest achievable check-to-WAL window without
			// taking ownership of or renaming the retired source. The adapter
			// mutex serializes host migrations; a malicious same-UID writer can
			// still race the final check and WAL append.
			return state.legacyMemory.validateFixedSnapshot(raw, archiveDigest)
		},
	})
}

func sameMigrationDescriptor(left, right artifacts.KindDescriptor) bool {
	return left.Kind == right.Kind && left.Schema == right.Schema &&
		left.Definition.Name == right.Definition.Name && left.Definition.SchemaDigest() == right.Definition.SchemaDigest()
}

func (s *Service) resolveLegacySessionAnchor(principal, subject string) (string, bool) {
	if strings.TrimSpace(subject) == "" {
		return "", false
	}
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	anchor := ""
	for _, session := range s.sessions {
		if session == nil || !session.scope.durable || session.principal != principal || session.scope.subject != subject {
			continue
		}
		if anchor != "" && anchor != session.handle.SessionID {
			return "", false
		}
		anchor = session.handle.SessionID
	}
	return anchor, anchor != ""
}

func (source *legacyMemorySource) readArchiveAndRecheckLocked() ([]byte, string, error) {
	if source == nil || source.root == nil {
		return nil, "", errors.New("legacy memory source unavailable")
	}
	raw, digest, err := source.readFixed()
	if err != nil {
		return nil, "", err
	}
	if err := source.preserveArchive(raw, digest); err != nil {
		return nil, "", err
	}
	// Re-read after the archive fsync. A racing append/replacement cannot be
	// silently omitted from the canonical transaction.
	recheck, recheckDigest, err := source.readFixed()
	if err != nil {
		return nil, "", err
	}
	if digest != recheckDigest || !bytesEqual(raw, recheck) {
		return nil, "", errors.New("legacy memory source changed while migration was archiving it")
	}
	return raw, digest, nil
}

func (source *legacyMemorySource) readFixed() ([]byte, string, error) {
	info, err := source.root.Lstat(legacyMemorySourcePath)
	if errors.Is(err, os.ErrNotExist) {
		empty := sha256.Sum256(nil)
		return nil, "sha256:" + hex.EncodeToString(empty[:]), nil
	}
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", errors.New("legacy memory source must be a regular non-symlink file")
	}
	if info.Size() > maxLegacyMemoryBytes {
		return nil, "", fmt.Errorf("legacy memory source exceeds %d bytes", maxLegacyMemoryBytes)
	}
	file, err := source.root.Open(legacyMemorySourcePath)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, "", errors.New("legacy memory source changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxLegacyMemoryBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) > maxLegacyMemoryBytes {
		return nil, "", fmt.Errorf("legacy memory source exceeds %d bytes", maxLegacyMemoryBytes)
	}
	after, err := file.Stat()
	if err != nil || after.Size() != int64(len(raw)) || !os.SameFile(opened, after) {
		return nil, "", errors.New("legacy memory source changed while reading")
	}
	sum := sha256.Sum256(raw)
	return raw, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (source *legacyMemorySource) validateFixedSnapshot(raw []byte, digest string) error {
	latest, latestDigest, err := source.readFixed()
	if err != nil {
		return err
	}
	if latestDigest != digest || !bytesEqual(latest, raw) {
		return errors.New("fixed legacy memory source no longer matches the fsynced archive")
	}
	return nil
}

func (source *legacyMemorySource) preserveArchive(raw []byte, digest string) error {
	if err := rejectSymlinkIfPresent(source.root, "memory"); err != nil {
		return err
	}
	if err := source.root.MkdirAll(legacyMemoryArchiveDir, 0o700); err != nil {
		return err
	}
	if err := rejectSymlinkIfPresent(source.root, legacyMemoryArchiveDir); err != nil {
		return err
	}
	if info, err := source.root.Lstat(legacyMemoryArchive); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("legacy memory archive must be a regular non-symlink file")
		}
		if info.Size() > maxLegacyMemoryBytes {
			return fmt.Errorf("legacy memory archive exceeds %d bytes", maxLegacyMemoryBytes)
		}
		existing, readErr := source.root.ReadFile(legacyMemoryArchive)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(existing)
		if "sha256:"+hex.EncodeToString(sum[:]) != digest || !bytesEqual(existing, raw) {
			return errors.New("legacy memory archive digest mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := legacyMemoryArchiveDir + "/.memory.jsonl." + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := source.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = source.root.Remove(tmp)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := source.root.Rename(tmp, legacyMemoryArchive); err != nil {
		return err
	}
	remove = false
	directory, err := source.root.Open(legacyMemoryArchiveDir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func rejectSymlinkIfPresent(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsDir() {
		return fmt.Errorf("legacy migration path %q must be a non-symlink directory", name)
	}
	return nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
