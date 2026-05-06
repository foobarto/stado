package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrNotFound is returned by Get when the named secret does not exist.
var ErrNotFound = errors.New("secrets: not found")

var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// ValidName returns nil when name is acceptable for use as a secret name:
// matches [a-zA-Z0-9_.-]+, length 1..128, no path separators, no leading
// dot, not the reserved ".." name.
func ValidName(name string) error {
	if name == "" {
		return errors.New("secrets: name must not be empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("secrets: name too long (%d bytes, max 128)", len(name))
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("secrets: name %q must not contain path separators", name)
	}
	if name == ".." {
		return fmt.Errorf("secrets: name %q is reserved", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("secrets: name %q must not begin with a dot", name)
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("secrets: name %q contains invalid characters (allowed: a-z A-Z 0-9 _ . -)", name)
	}
	return nil
}

// Store is the on-disk operator secret store. Files live at
// <stateDir>/secrets/<name>; each file holds the raw bytes for one secret.
// Mode 0600, owner-only.
type Store struct{ root string }

// NewStore returns a Store rooted at <stateDir>/secrets.
func NewStore(stateDir string) *Store {
	return &Store{root: filepath.Join(stateDir, "secrets")}
}

// Get reads the named secret. Returns (nil, ErrNotFound) when the secret
// doesn't exist. Returns an error if the file's permissions are wider than
// 0600 — the operator must chmod the file before stado will read it.
func (s *Store) Get(name string) ([]byte, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("secrets: stat %s: %w", name, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		return nil, fmt.Errorf("secrets: refusing to read %s: permissions are %04o, expected 0600 (operator may need to chmod 0600)", name, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("secrets: read %s: %w", name, err)
	}
	return data, nil
}

// Put writes the named secret atomically (write-then-rename) and chmods to
// 0600. Idempotent — calling Put again with the same name overwrites.
func (s *Store) Put(name string, value []byte) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("secrets: create secrets dir: %w", err)
	}
	tmp := filepath.Join(s.root, "."+name+".tmp")
	if err := os.WriteFile(tmp, value, 0o600); err != nil {
		return fmt.Errorf("secrets: write temp for %s: %w", name, err)
	}
	// Ensure mode is 0600 even if umask widens it.
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: chmod temp for %s: %w", name, err)
	}
	dest := filepath.Join(s.root, name)
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: rename into place for %s: %w", name, err)
	}
	return nil
}

// List returns the sorted set of secret names. Returns an empty slice when
// the secrets directory does not exist yet.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("secrets: list: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		// Skip temp files written by Put.
		if strings.HasPrefix(n, ".") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes the named secret. Idempotent — missing secret is not an
// error.
func (s *Store) Remove(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	path := filepath.Join(s.root, name)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets: remove %s: %w", name, err)
	}
	return nil
}
