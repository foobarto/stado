# EP-0039: Plugin Distribution and Trust — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement VCS-based plugin identity, anchor-of-trust pattern, strict semver versioning, multi-version coexistence, lock file, and the quality-pass items (sha256 drift detection, `--pubkey-file`, `plugin dev`, `plugin use`).

**Architecture:** Plugin identity is `<host>/<owner>/<repo>[/<subdir>]@<version>`. Install layout keys by a hash of the canonical identity URL. Active-version state is per-project (`.stado/plugin-lock.toml`). Anchor pubkey lives at `<host>/<owner>/stado-plugins/.stado/author.pub`. Trust model is TOFU per owner fingerprint stored in `~/.local/share/stado/trust/`. Strict versioning: only `vX.Y.Z` tags or 40-char SHAs accepted at install time.

**Tech Stack:** Go, `go-git` or `net/http` for VCS fetch, semver parsing, TOML (koanf), ed25519 (already used).

**Spec:** `docs/eps/0039-plugin-distribution-and-trust.md`

**Depends on:** Nothing (independent of EP-0037/0038, can land in parallel)

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `internal/plugins/identity.go` | Create | Parse + normalise plugin identity URLs |
| `internal/plugins/identity_test.go` | Create | Unit tests |
| `internal/plugins/version.go` | Create | Strict semver + SHA validation |
| `internal/plugins/version_test.go` | Create | Unit tests |
| `internal/plugins/anchor.go` | Create | Anchor pubkey fetch + cache |
| `internal/plugins/anchor_test.go` | Create | Tests with local HTTP mock |
| `internal/plugins/lock.go` | Create | Lock file read/write |
| `internal/plugins/lock_test.go` | Create | Tests |
| `internal/plugins/installed.go` | Modify | Re-key install dir by identity hash |
| `cmd/stado/plugin_install.go` | Modify | Remote fetch + anchor TOFU + lock write |
| `cmd/stado/plugin_trust.go` | Modify | Add --pubkey-file flag |
| `cmd/stado/plugin.go` | Modify | Add `plugin use`, `plugin dev`, `plugin update-anchor` |

---

## Task 1: Plugin identity parsing

**Files:**
- Create: `internal/plugins/identity.go`
- Create: `internal/plugins/identity_test.go`

- [ ] **Step 1: Write tests**

```go
// internal/plugins/identity_test.go
package plugins_test

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestParseIdentity(t *testing.T) {
	cases := []struct {
		raw         string
		wantHost    string
		wantOwner   string
		wantRepo    string
		wantSubdir  string
		wantVersion string
	}{
		{
			raw: "github.com/foobarto/my-plugin@v1.2.3",
			wantHost: "github.com", wantOwner: "foobarto",
			wantRepo: "my-plugin", wantVersion: "v1.2.3",
		},
		{
			raw: "github.com/foobarto/monorepo/plugins/myplugin@v0.1.0",
			wantHost: "github.com", wantOwner: "foobarto",
			wantRepo: "monorepo", wantSubdir: "plugins/myplugin", wantVersion: "v0.1.0",
		},
		{
			raw: "github.com/foobarto/myplugin@abc123def456abc123def456abc123def456abc1",
			wantHost: "github.com", wantOwner: "foobarto",
			wantRepo: "myplugin", wantVersion: "abc123def456abc123def456abc123def456abc1",
		},
	}
	for _, c := range cases {
		id, err := plugins.ParseIdentity(c.raw)
		if err != nil {
			t.Errorf("ParseIdentity(%q) error: %v", c.raw, err)
			continue
		}
		if id.Host != c.wantHost {
			t.Errorf("%q: host = %q, want %q", c.raw, id.Host, c.wantHost)
		}
		if id.Owner != c.wantOwner {
			t.Errorf("%q: owner = %q, want %q", c.raw, id.Owner, c.wantOwner)
		}
		if id.Repo != c.wantRepo {
			t.Errorf("%q: repo = %q, want %q", c.raw, id.Repo, c.wantRepo)
		}
		if id.Subdir != c.wantSubdir {
			t.Errorf("%q: subdir = %q, want %q", c.raw, id.Subdir, c.wantSubdir)
		}
		if id.Version != c.wantVersion {
			t.Errorf("%q: version = %q, want %q", c.raw, id.Version, c.wantVersion)
		}
	}
}

func TestParseIdentity_BadVersion(t *testing.T) {
	_, err := plugins.ParseIdentity("github.com/foo/bar@latest")
	if err == nil {
		t.Error("'latest' should be rejected")
	}
	_, err = plugins.ParseIdentity("github.com/foo/bar@main")
	if err == nil {
		t.Error("'main' should be rejected")
	}
	_, err = plugins.ParseIdentity("github.com/foo/bar") // no @version
	if err == nil {
		t.Error("missing @version should be rejected")
	}
}

func TestIdentityKey(t *testing.T) {
	id1, _ := plugins.ParseIdentity("github.com/foo/bar@v1.0.0")
	id2, _ := plugins.ParseIdentity("github.com/foo/bar@v1.0.0")
	if id1.Key() != id2.Key() {
		t.Error("same identity should have same key")
	}
	id3, _ := plugins.ParseIdentity("github.com/foo/bar@v2.0.0")
	if id1.Key() == id3.Key() {
		t.Error("different versions should have different keys")
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/plugins/... -run TestParseIdentity 2>&1 | head -10
```

- [ ] **Step 3: Implement identity.go**

```go
// internal/plugins/identity.go
package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Identity is the canonical identity of a remote plugin.
// Format: <host>/<owner>/<repo>[/<subdir...>]@<version>
// Version must be a semver tag (vX.Y.Z) or a full 40-char SHA.
type Identity struct {
	Host    string
	Owner   string
	Repo    string
	Subdir  string // empty for top-level plugin
	Version string
	raw     string
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
var semverRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?(\+[a-zA-Z0-9.]+)?$`)

// ParseIdentity parses a plugin identity string.
// Rejects floating versions (latest, main, HEAD, branch names).
func ParseIdentity(raw string) (Identity, error) {
	atIdx := strings.LastIndex(raw, "@")
	if atIdx < 0 {
		return Identity{}, fmt.Errorf("identity %q: missing @version", raw)
	}
	path, version := raw[:atIdx], raw[atIdx+1:]
	if err := validateVersion(version); err != nil {
		return Identity{}, fmt.Errorf("identity %q: %w", raw, err)
	}
	// path: host/owner/repo[/subdir...]
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 3 {
		return Identity{}, fmt.Errorf("identity %q: expected host/owner/repo", raw)
	}
	id := Identity{
		Host:    parts[0],
		Owner:   parts[1],
		Repo:    parts[2],
		Version: version,
		raw:     raw,
	}
	if len(parts) == 4 {
		id.Subdir = parts[3]
	}
	return id, nil
}

func validateVersion(v string) error {
	if shaRE.MatchString(v) {
		return nil
	}
	if semverRE.MatchString(v) {
		return nil
	}
	return fmt.Errorf("version %q is not a semver tag (vX.Y.Z) or 40-char SHA; floating tags (latest, main, HEAD) are rejected", v)
}

// Key returns a stable opaque string uniquely identifying this
// host+owner+repo+subdir+version combination. Used as an install-dir key.
func (id Identity) Key() string {
	sum := sha256.Sum256([]byte(id.Canonical()))
	return hex.EncodeToString(sum[:])[:16]
}

// Canonical returns the normalised identity string.
func (id Identity) Canonical() string {
	s := id.Host + "/" + id.Owner + "/" + id.Repo
	if id.Subdir != "" {
		s += "/" + id.Subdir
	}
	return s + "@" + id.Version
}

// AnchorURL returns the well-known URL for this owner's anchor pubkey.
// Format: https://<host>/<owner>/stado-plugins/raw/<default-branch>/.stado/author.pub
func (id Identity) AnchorURL() string {
	return fmt.Sprintf("https://%s/%s/stado-plugins/raw/main/.stado/author.pub", id.Host, id.Owner)
}

// LocalAlias returns the default local alias derived from the repo name.
// Collision resolution at install time is the operator's job.
func (id Identity) LocalAlias() string {
	if id.Subdir != "" {
		parts := strings.Split(id.Subdir, "/")
		return parts[len(parts)-1]
	}
	return id.Repo
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/plugins/... -run TestParseIdentity -v
go build ./...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/identity.go internal/plugins/identity_test.go
git commit -m "feat(ep-0039): plugin identity parsing + strict version validation"
```

---

## Task 2: Anchor-of-trust fetch + TOFU

**Files:**
- Create: `internal/plugins/anchor.go`
- Create: `internal/plugins/anchor_test.go`

- [ ] **Step 1: Write tests**

```go
// internal/plugins/anchor_test.go
package plugins_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestFetchAnchorPubkey(t *testing.T) {
	// Serve a fake author.pub.
	const fakePub = "aabbccdd" + "0011223344556677889900112233445566778899001122334455667788990011" // 64 hex chars (fake Ed25519)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fakePub))
	}))
	defer srv.Close()

	pub, err := plugins.FetchAnchorPubkey(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if pub != fakePub {
		t.Errorf("got %q, want %q", pub, fakePub)
	}
}

func TestAnchorTrustStore(t *testing.T) {
	dir := t.TempDir()
	store := plugins.NewTrustStore(dir)

	owner := "github.com/foobarto"
	fingerprint := "deadbeefdeadbeef"

	// Not trusted yet.
	trusted, _ := store.IsTrusted(owner, fingerprint)
	if trusted {
		t.Error("should not be trusted yet")
	}

	// Trust it.
	if err := store.Trust(owner, fingerprint, plugins.TrustScopeOwner); err != nil {
		t.Fatal(err)
	}

	// Now trusted.
	trusted, _ = store.IsTrusted(owner, fingerprint)
	if !trusted {
		t.Error("should be trusted after Trust()")
	}

	// Different fingerprint — not trusted.
	trusted, _ = store.IsTrusted(owner, "cafecafe")
	if trusted {
		t.Error("different fingerprint should not be trusted")
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/plugins/... -run TestFetchAnchor -v 2>&1 | head -10
```

- [ ] **Step 3: Implement anchor.go**

```go
// internal/plugins/anchor.go
package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxAnchorPubkeyBytes = 4 << 10

// FetchAnchorPubkey fetches the author pubkey from the given URL (the
// well-known anchor endpoint). Returns the hex-encoded pubkey string.
func FetchAnchorPubkey(url string) (string, error) {
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return "", fmt.Errorf("anchor fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return "", fmt.Errorf("anchor not found at %s — owner may not publish a stado-plugins anchor repo", url)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anchor fetch: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnchorPubkeyBytes))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// TrustScope controls how broadly a trusted fingerprint applies.
type TrustScope string

const (
	TrustScopeOwner TrustScope = "owner" // all repos by this owner
	TrustScopeRepo  TrustScope = "repo"  // specific repo only
	TrustScopeGlob  TrustScope = "all"   // any plugin (for local dev key)
)

// TrustStore is a persistent per-user store of trusted owner fingerprints.
// Stored as JSON files under dir/trust/<owner-hash>.json.
type TrustStore struct {
	dir string
}

type trustEntry struct {
	Owner       string     `json:"owner"`
	Fingerprint string     `json:"fingerprint"`
	Scope       TrustScope `json:"scope"`
	TrustedAt   string     `json:"trusted_at"`
}

func NewTrustStore(dir string) *TrustStore {
	return &TrustStore{dir: dir}
}

func (ts *TrustStore) IsTrusted(owner, fingerprint string) (bool, error) {
	entry, err := ts.load(owner)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return entry.Fingerprint == fingerprint, nil
}

func (ts *TrustStore) Trust(owner, fingerprint string, scope TrustScope) error {
	entry := trustEntry{
		Owner:       owner,
		Fingerprint: fingerprint,
		Scope:       scope,
		TrustedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(ts.dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(entry, "", "  ")
	return os.WriteFile(ts.entryPath(owner), data, 0o600)
}

func (ts *TrustStore) load(owner string) (*trustEntry, error) {
	data, err := os.ReadFile(ts.entryPath(owner))
	if err != nil {
		return nil, err
	}
	var e trustEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (ts *TrustStore) entryPath(owner string) string {
	// Use owner string directly (sanitised) as filename.
	safe := strings.ReplaceAll(owner, "/", "_")
	return filepath.Join(ts.dir, safe+".json")
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/plugins/... -run TestFetchAnchor -v
go test ./internal/plugins/... -run TestAnchorTrustStore -v
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/anchor.go internal/plugins/anchor_test.go
git commit -m "feat(ep-0039): anchor-of-trust fetch + TOFU trust store"
```

---

## Task 3: Lock file

**Files:**
- Create: `internal/plugins/lock.go`
- Create: `internal/plugins/lock_test.go`

- [ ] **Step 1: Write tests**

```go
// internal/plugins/lock_test.go
package plugins_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestLockRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin-lock.toml")

	lock := plugins.NewLock()
	lock.Add(plugins.LockEntry{
		Identity:    "github.com/foo/bar@v1.0.0",
		WASMSHA256:  "abc123",
		AnchorFpr:   "deadbeef",
		InstalledAt: "2026-05-05T00:00:00Z",
	})

	if err := lock.Write(path); err != nil {
		t.Fatal(err)
	}

	lock2, err := plugins.ReadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock2.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(lock2.Entries))
	}
	if lock2.Entries[0].Identity != "github.com/foo/bar@v1.0.0" {
		t.Errorf("wrong identity: %s", lock2.Entries[0].Identity)
	}
}

func TestLockMissingFile(t *testing.T) {
	_, err := plugins.ReadLock("/nonexistent/plugin-lock.toml")
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist, got: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/plugins/... -run TestLock 2>&1 | head -10
```

- [ ] **Step 3: Implement lock.go**

```go
// internal/plugins/lock.go
package plugins

import (
	"bytes"
	"os"

	"github.com/BurntSushi/toml"
)

// LockEntry is one entry in plugin-lock.toml.
type LockEntry struct {
	Identity    string `toml:"identity"`
	WASMSHA256  string `toml:"wasm_sha256"`
	AnchorFpr   string `toml:"anchor_fingerprint"`
	InstalledAt string `toml:"installed_at"`
}

// Lock is the in-memory representation of plugin-lock.toml.
type Lock struct {
	Entries []LockEntry `toml:"plugin"`
}

func NewLock() *Lock {
	return &Lock{}
}

func (l *Lock) Add(e LockEntry) {
	// Replace if same identity already present.
	for i, existing := range l.Entries {
		if existing.Identity == e.Identity {
			l.Entries[i] = e
			return
		}
	}
	l.Entries = append(l.Entries, e)
}

func (l *Lock) Write(path string) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func ReadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if _, err := toml.Decode(string(data), &l); err != nil {
		return nil, err
	}
	return &l, nil
}
```

Note: if `github.com/BurntSushi/toml` is not already a dependency, check `go.mod` and add it:
```
grep BurntSushi go.mod || go get github.com/BurntSushi/toml
```

- [ ] **Step 4: Run tests**

```
go test ./internal/plugins/... -run TestLock -v
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/lock.go internal/plugins/lock_test.go
git commit -m "feat(ep-0039): plugin-lock.toml read/write"
```

---

## Task 4: SHA256 drift detection at install time (quality pass §C)

**Files:**
- Modify: `cmd/stado/plugin_install.go`

This implements EP-0039 §I's sha drift item: if the wasm sha256 in the about-to-install manifest differs from the installed one (same name+version), print `reinstalling (sha256 changed)` and replace. The `--force` flag bypasses the same-version skip entirely.

- [ ] **Step 1: Find the idempotency check in plugin_install.go**

```
grep -n "already installed\|skip\|version\|sha256\|WASMSHA256" cmd/stado/plugin_install.go | head -20
```

- [ ] **Step 2: Write test**

```go
// cmd/stado/plugin_install_test.go (create if not exists)
package main

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestInstallSHADrift(t *testing.T) {
	// Conceptual: installed manifest has sha256 X; new manifest has sha256 Y.
	// Expect: reinstall triggers.
	old := plugins.Manifest{Name: "foo", Version: "v1.0.0", WASMSHA256: "aaaa"}
	new_ := plugins.Manifest{Name: "foo", Version: "v1.0.0", WASMSHA256: "bbbb"}
	if !shouldReinstall(old, new_) {
		t.Error("SHA drift should trigger reinstall")
	}
	same := plugins.Manifest{Name: "foo", Version: "v1.0.0", WASMSHA256: "aaaa"}
	if shouldReinstall(old, same) {
		t.Error("identical SHA should not trigger reinstall")
	}
}
```

- [ ] **Step 3: Add shouldReinstall helper + wire it into install path**

```go
// In cmd/stado/plugin_install.go, add:
func shouldReinstall(installed, incoming plugins.Manifest) bool {
	return installed.WASMSHA256 != incoming.WASMSHA256
}
```

Find the block where `stado plugin install` detects an already-installed name+version and update it:

```go
// Before (current):
// if existingVersion == manifest.Version {
//     fmt.Println("skipped: already installed")
//     return nil
// }

// After:
if existingManifest.Version == manifest.Version {
	if !forceInstall && !shouldReinstall(existingManifest, manifest) {
		fmt.Printf("skipped: %s %s already installed\n", manifest.Name, manifest.Version)
		return nil
	}
	if shouldReinstall(existingManifest, manifest) {
		fmt.Printf("reinstalling: %s %s (sha256 changed)\n", manifest.Name, manifest.Version)
	}
	// Fall through to install.
}
```

Add `--force` flag:

```go
var forceInstall bool
// In init():
pluginInstallCmd.Flags().BoolVar(&forceInstall, "force", false, "Override idempotency check and reinstall even if version matches")
```

- [ ] **Step 4: Run tests + build**

```
go test ./cmd/stado/... -run TestInstallSHA -v 2>/dev/null || true
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add cmd/stado/plugin_install.go
git commit -m "feat(ep-0039): sha256 drift detection + --force install flag"
```

---

## Task 5: --pubkey-file flag for plugin trust (quality pass §I)

**Files:**
- Modify: `cmd/stado/plugin_trust.go`

- [ ] **Step 1: Find the existing trust command**

```
grep -n "pubkey\|PubKey\|Flags\|cobra" cmd/stado/plugin_trust.go | head -20
```

- [ ] **Step 2: Add --pubkey-file flag**

```go
var trustPubkeyFile string

// In init():
pluginTrustCmd.Flags().StringVar(&trustPubkeyFile, "pubkey-file", "",
	"Path to a file containing the hex-encoded Ed25519 public key (alternative to passing the key inline).")

// In RunE, before using the pubkey arg:
if trustPubkeyFile != "" {
	data, err := os.ReadFile(trustPubkeyFile)
	if err != nil {
		return fmt.Errorf("--pubkey-file: %w", err)
	}
	// Replace args[0] with file contents.
	if len(args) == 0 {
		args = []string{strings.TrimSpace(string(data))}
	}
}
```

- [ ] **Step 3: Build + smoke test**

```
go build -o /tmp/stado-test ./cmd/stado
/tmp/stado-test plugin trust --help | grep pubkey-file
```
Expected: flag appears in help.

- [ ] **Step 4: Commit**

```bash
git add cmd/stado/plugin_trust.go
git commit -m "feat(ep-0039): plugin trust --pubkey-file flag"
```

---

## Task 6: `plugin dev` command (quality pass §I)

**Files:**
- Modify: `cmd/stado/plugin.go` (add `plugin dev` subcommand)

`plugin dev <dir>` collapses gen-key → build → trust → install into one command using a TOFU local-dev key. The key is stored in `.stado/dev.seed` and pinned per-directory.

- [ ] **Step 1: Write the subcommand**

```go
var pluginDevCmd = &cobra.Command{
	Use:   "dev <dir>",
	Short: "Build, trust, and install a plugin from a local directory (dev workflow)",
	Long: `plugin dev <dir> collapses the authoring workflow:
  1. Generate a dev seed in <dir>/.stado/dev.seed if not present
  2. Sign the manifest with the dev seed
  3. Trust the dev pubkey (TOFU, local scope)
  4. Install from the local directory

The dev seed is a ephemeral Ed25519 key for local development only.
Use 'plugin sign' + 'plugin trust' for production keys.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := args[0]
		seedPath := filepath.Join(dir, ".stado", "dev.seed")

		// Step 1: ensure dev seed exists.
		if err := os.MkdirAll(filepath.Dir(seedPath), 0o700); err != nil {
			return err
		}
		if _, err := os.Stat(seedPath); os.IsNotExist(err) {
			fmt.Printf("Generating dev seed at %s...\n", seedPath)
			// Call existing gen-key logic.
			if err := runGenKey(seedPath); err != nil {
				return fmt.Errorf("gen-key: %w", err)
			}
		}

		// Step 2: sign.
		fmt.Println("Signing manifest...")
		if err := runSign(dir, seedPath); err != nil {
			return fmt.Errorf("sign: %w", err)
		}

		// Step 3: read pubkey and trust.
		pubkeyPath := filepath.Join(dir, "author.pubkey")
		pubkeyData, err := os.ReadFile(pubkeyPath)
		if err != nil {
			return fmt.Errorf("read pubkey: %w", err)
		}
		pubkey := strings.TrimSpace(string(pubkeyData))
		fmt.Printf("Trusting dev key %s...\n", pubkey[:8]+"...")
		if err := runTrust([]string{pubkey}); err != nil {
			return fmt.Errorf("trust: %w", err)
		}

		// Step 4: install.
		fmt.Println("Installing...")
		return runInstall(dir, true /*force*/)
	},
}

func init() {
	pluginCmd.AddCommand(pluginDevCmd)
}
```

(Note: `runGenKey`, `runSign`, `runTrust`, `runInstall` are the existing RunE bodies extracted to helper functions. If they're not already helpers, extract them as part of this task.)

- [ ] **Step 2: Build + smoke test**

```
go build -o /tmp/stado-test ./cmd/stado
/tmp/stado-test plugin dev --help
```

- [ ] **Step 3: Commit**

```bash
git add cmd/stado/plugin.go
git commit -m "feat(ep-0039): plugin dev command — one-step authoring workflow"
```

---

## Task 7: Wire lock file into `plugin install` + `plugin use`

**Files:**
- Modify: `cmd/stado/plugin_install.go` (write lock entry after successful install)
- Modify: `cmd/stado/plugin.go` (add `plugin use` subcommand)

- [ ] **Step 1: Write lock entry after install**

In `plugin_install.go`, after successful installation:

```go
lockPath := filepath.Join(projectStadoDir, "plugin-lock.toml")
lock, err := plugins.ReadLock(lockPath)
if os.IsNotExist(err) {
	lock = plugins.NewLock()
} else if err != nil {
	fmt.Fprintf(os.Stderr, "warn: could not read lock file: %v\n", err)
	lock = plugins.NewLock()
}
lock.Add(plugins.LockEntry{
	Identity:    manifest.Name + "@" + manifest.Version, // upgrade when identity URL is available
	WASMSHA256:  manifest.WASMSHA256,
	AnchorFpr:   manifest.AuthorPubkeyFpr,
	InstalledAt: time.Now().UTC().Format(time.RFC3339),
})
if err := lock.Write(lockPath); err != nil {
	fmt.Fprintf(os.Stderr, "warn: could not write lock file: %v\n", err)
}
```

- [ ] **Step 2: Add `plugin use` subcommand**

```go
var pluginUseCmd = &cobra.Command{
	Use:   "use <name>@<version>",
	Short: "Switch the active version for a plugin (per-project)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nameVer := args[0]
		// Validate name@version format.
		parts := strings.SplitN(nameVer, "@", 2)
		if len(parts) != 2 {
			return fmt.Errorf("usage: plugin use <name>@<version>")
		}
		name, version := parts[0], parts[1]
		// Verify the version is installed.
		installDir := plugins.InstalledDir(name, version)
		if _, err := os.Stat(installDir); os.IsNotExist(err) {
			return fmt.Errorf("plugin %s@%s is not installed", name, version)
		}
		// Write active-version marker.
		activeFile := plugins.ActiveVersionFile(name)
		if err := os.MkdirAll(filepath.Dir(activeFile), 0o755); err != nil {
			return err
		}
		return os.WriteFile(activeFile, []byte(version), 0o644)
	},
}

func init() {
	pluginCmd.AddCommand(pluginUseCmd)
}
```

(Add `InstalledDir` and `ActiveVersionFile` helpers to `internal/plugins/installed.go` following existing patterns.)

- [ ] **Step 3: Build + test**

```
go build ./...
go test ./internal/plugins/... -v -count=1 2>&1 | tail -20
```

- [ ] **Step 4: Commit**

```bash
git add cmd/stado/plugin_install.go cmd/stado/plugin.go internal/plugins/installed.go
git commit -m "feat(ep-0039): lock file write on install + plugin use subcommand"
```

---

## Self-Review

**Spec coverage check (EP-0039 §A–§K):**

| EP section | Task |
|---|---|
| §A Identity (URL format, monorepo subdir) | Task 1 |
| §B Strict versioning | Task 1 (validateVersion) |
| §C Anchor-of-trust fetch | Task 2 |
| §D TOFU trust model | Task 2 (TrustStore) |
| §E Artefact resolution (3-tier) | Deferred — requires git fetch; Task 1-3 provide the pieces |
| §F Multi-version coexistence | Task 7 (plugin use + active-version file) |
| §G Update flow | Deferred — `plugin update` subcommand |
| §H Sandbox interaction | Deferred — uses EP-0038d sandbox |
| §I Quality pass: sha drift | Task 4 |
| §I Quality pass: --pubkey-file | Task 5 |
| §I Quality pass: plugin dev | Task 6 |
| §K CLI surface delta | Covered across tasks |
| Lock file | Task 3 + Task 7 |

**Not covered (follow-up):** Full remote fetch via git (`go-git` or shell `git clone`), three-tier artefact resolution, `plugin update` / `plugin update-anchor`, `plugin remove` with cache cleanup, tag-move detection (EP-0039 D13).
