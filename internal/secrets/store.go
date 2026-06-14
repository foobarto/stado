package secrets

import (
	"crypto/sha256"
	"encoding/hex"
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
	return readSecretFile(filepath.Join(s.root, name), name)
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

// ── Per-plugin scoping (EP-0038 D19) ────────────────────────────────────────
//
// The flat Get/Put/List/Remove above are the OPERATOR surface (the
// `stado secrets` CLI): one shared keyspace the operator provisions. Plugins
// must NOT share that keyspace by name — plugin A writing "token" must not be
// readable, overwritable, or deletable by plugin B. The *Scoped variants give
// each plugin its own keyspace under <root>/.plugins/<plugin>/, while reads
// fall back to the shared operator keyspace so an operator-provisioned secret
// (e.g. an API key) stays readable by any plugin granted the cap. The scope
// dir is dot-prefixed so it can never collide with a shared secret name
// (ValidName forbids a leading dot) and the operator-facing List skips it.

// pluginScopeRoot returns <root>/.plugins/<segment>, where segment is a
// path-safe, collision-free encoding of the plugin name: the name verbatim
// when it's already a safe single segment, else its sha256 hex (covers
// canonical identities like "github.com/owner/repo" that contain separators).
func (s *Store) pluginScopeRoot(plugin string) string {
	seg := plugin
	if ValidName(plugin) != nil {
		sum := sha256.Sum256([]byte(plugin))
		seg = hex.EncodeToString(sum[:])
	}
	return filepath.Join(s.root, ".plugins", seg)
}

// GetScoped reads a secret for a plugin: its own scoped copy first, then the
// shared operator keyspace. (nil, ErrNotFound) when neither exists.
func (s *Store) GetScoped(plugin, name string) ([]byte, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	scoped := filepath.Join(s.pluginScopeRoot(plugin), name)
	if data, err := readSecretFile(scoped, name); err == nil {
		return data, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	// Fall back to the shared operator keyspace (also covers secrets written
	// by older stado versions before scoping existed).
	return s.Get(name)
}

// PutScoped writes a secret into the plugin's own scope — never the shared
// operator keyspace, so a plugin can't overwrite an operator secret.
func (s *Store) PutScoped(plugin, name string, value []byte) error {
	if err := ValidName(name); err != nil {
		return err
	}
	dir := s.pluginScopeRoot(plugin)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: create plugin scope dir: %w", err)
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, value, 0o600); err != nil {
		return fmt.Errorf("secrets: write temp for %s: %w", name, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: chmod temp for %s: %w", name, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: rename into place for %s: %w", name, err)
	}
	return nil
}

// ListScoped returns the names a plugin can see: its own scoped secrets unioned
// with the shared operator keyspace, sorted and deduped.
func (s *Store) ListScoped(plugin string) ([]string, error) {
	shared, err := s.List()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range shared {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	entries, err := os.ReadDir(s.pluginScopeRoot(plugin))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("secrets: list scoped: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !seen[e.Name()] {
			seen[e.Name()] = true
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// RemoveScoped deletes a plugin's own scoped secret only — it never deletes a
// shared operator secret (idempotent; missing is not an error).
func (s *Store) RemoveScoped(plugin, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.pluginScopeRoot(plugin), name))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets: remove scoped %s: %w", name, err)
	}
	return nil
}

// readSecretFile reads one secret file enforcing the 0600 permission gate.
// Shared by Get and GetScoped.
func readSecretFile(path, name string) ([]byte, error) {
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
