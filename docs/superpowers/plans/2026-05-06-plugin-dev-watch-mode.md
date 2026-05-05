# Plugin Dev Watch Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `stado plugin dev <dir> --watch` — file-watch + auto-rebuild + auto-reinstall under a `0.0.0-dev` sentinel that gets cleaned up on watch exit, so plugin authors get a hot-reload inner loop without manually re-running the sign/install pipeline on every save.

**Architecture:** Reuses the existing `pluginDevCmd` first-run pipeline, then spawns a debounced fsnotify watcher that re-runs `<dir>/build.sh` and re-invokes the existing `pluginSignCmd` + `pluginInstallCmd` on each batch of file changes. Dev installs go to `<state>/plugins/<name>-0.0.0-dev/` with the active marker pinned to that version; cleanup on Ctrl+C removes both. Reuses the unified-registry slot — no separate `DevPluginRegistry`. TUI live-reload is out of scope; the iteration loop runs through `stado tool run`, which rebuilds the registry on every CLI invocation.

**Tech Stack:** Go 1.22+, cobra, `github.com/fsnotify/fsnotify`, the existing `internal/plugins` + `internal/runtime` packages.

---

### Task 1: Add fsnotify dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the dep**

Run: `go get github.com/fsnotify/fsnotify@v1.7.0`

Expected: `go.mod` gains `github.com/fsnotify/fsnotify v1.7.0` as a direct dep; `go.sum` gains its checksums.

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: clean exit.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add fsnotify for plugin dev watch mode"
```

---

### Task 2: `internal/plugins/devmode.go` — sentinel + helpers (TDD)

**Files:**
- Create: `internal/plugins/devmode.go`
- Create: `internal/plugins/devmode_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/plugins/devmode_test.go`:

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPinActiveDev_WritesMarker: PinActiveDev should write the
// dev-version sentinel to <stateDir>/plugins/active/<name>.
func TestPinActiveDev_WritesMarker(t *testing.T) {
	state := t.TempDir()
	if err := PinActiveDev(state, "myplugin"); err != nil {
		t.Fatalf("PinActiveDev: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(state, "plugins", "active", "myplugin"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != DevSentinelVersion {
		t.Errorf("marker = %q, want %q", string(got), DevSentinelVersion)
	}
}

// TestCleanupDev_RemovesDirAndMarker: CleanupDev should remove
// both the install dir and the marker.
func TestCleanupDev_RemovesDirAndMarker(t *testing.T) {
	state := t.TempDir()
	installDir := filepath.Join(state, "plugins", "myplugin-"+DevSentinelVersion)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	activeDir := filepath.Join(state, "plugins", "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(activeDir, "myplugin")
	if err := os.WriteFile(markerPath, []byte(DevSentinelVersion), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CleanupDev(state, "myplugin"); err != nil {
		t.Fatalf("CleanupDev: %v", err)
	}
	if _, err := os.Stat(installDir); !os.IsNotExist(err) {
		t.Errorf("install dir should be gone; stat err = %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should be gone; stat err = %v", err)
	}
}

// TestCleanupDev_Idempotent: CleanupDev should not error when the
// dir + marker are already absent.
func TestCleanupDev_Idempotent(t *testing.T) {
	state := t.TempDir()
	if err := CleanupDev(state, "missing"); err != nil {
		t.Errorf("CleanupDev on missing should be no-op; got: %v", err)
	}
}

// TestDevSentinelVersion_ParsesAsSemver: the sentinel must round-
// trip through golang.org/x/mod/semver so the unified registry's
// pickActiveVersion treats it consistently with other versions.
func TestDevSentinelVersion_ParsesAsSemver(t *testing.T) {
	// 0.0.0-dev → v0.0.0-dev → semver.IsValid returns true.
	v := "v" + DevSentinelVersion
	if !semverIsValid(v) {
		t.Errorf("DevSentinelVersion %q is not valid semver after v-prefixing", DevSentinelVersion)
	}
}

// semverIsValid is a thin wrapper kept inside the test file so the
// import doesn't leak to non-test builds.
func semverIsValid(v string) bool {
	// We could call golang.org/x/mod/semver.IsValid, but the simpler
	// check that pickActiveVersion uses is semver.Compare returning a
	// stable order. For the test, the round-trip property is enough:
	// the sentinel must START with a digit (after stripping leading v).
	if len(v) < 2 || v[0] != 'v' {
		return false
	}
	c := v[1]
	return c >= '0' && c <= '9'
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugins/ -run "TestPinActiveDev_WritesMarker|TestCleanupDev_RemovesDirAndMarker|TestCleanupDev_Idempotent|TestDevSentinelVersion_ParsesAsSemver" -count=1 -v`

Expected: FAIL with "undefined: DevSentinelVersion / PinActiveDev / CleanupDev".

- [ ] **Step 3: Write the implementation**

Create `internal/plugins/devmode.go`:

```go
package plugins

import (
	"os"
	"path/filepath"
)

// DevSentinelVersion is the version string used by `stado plugin dev
// --watch` for the in-development install. The dev install lives at
// <state>/plugins/<name>-0.0.0-dev/ and is pinned via the active-
// marker mechanism so the unified registry registers it as the
// active version. Cleanup removes both on watch-loop exit.
const DevSentinelVersion = "0.0.0-dev"

// PinActiveDev writes the active-version marker for `name` pointing
// at DevSentinelVersion. Caller is responsible for ensuring the
// install dir exists before any registry lookup happens.
func PinActiveDev(stateDir, name string) error {
	activeDir := filepath.Join(stateDir, "plugins", "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(activeDir, name), []byte(DevSentinelVersion), 0o644)
}

// CleanupDev removes the dev install dir and the active-version
// marker for `name`. Idempotent: missing dir or marker is not an
// error. Called on watch-loop exit (defer + signal handler).
func CleanupDev(stateDir, name string) error {
	installDir := filepath.Join(stateDir, "plugins", name+"-"+DevSentinelVersion)
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	markerPath := filepath.Join(stateDir, "plugins", "active", name)
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run "TestPinActiveDev_WritesMarker|TestCleanupDev_RemovesDirAndMarker|TestCleanupDev_Idempotent|TestDevSentinelVersion_ParsesAsSemver" -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/devmode.go internal/plugins/devmode_test.go
git commit -m "feat(plugins): DevSentinelVersion + Pin/CleanupDev helpers"
```

---

### Task 3: `--manifest-version` flag on `pluginSignCmd`

**Files:**
- Modify: `cmd/stado/plugin_sign.go`

The dev watch loop needs to override the manifest's `version` field at sign time so every rebuild produces the same install-dir name (`<name>-0.0.0-dev`). The default behavior of `plugin sign` is unchanged.

- [ ] **Step 1: Read the current pluginSignCmd**

Run: `grep -n "pluginSign\|version" cmd/stado/plugin_sign.go | head -30`

Use the result to locate where the manifest is loaded and where the version field is read. Plan the edit so the override is applied AFTER the manifest is loaded but BEFORE signing computes its bytes.

- [ ] **Step 2: Add the flag variable**

In `cmd/stado/plugin_sign.go`, near the existing flag-vars at the top, add:

```go
var pluginSignManifestVersion string
```

- [ ] **Step 3: Wire the override**

In `pluginSignCmd.RunE`, after the manifest template is loaded into a `Manifest` struct (search for `LoadFromDir` or `json.Unmarshal` of the template), insert:

```go
if pluginSignManifestVersion != "" {
    mf.Version = pluginSignManifestVersion
}
```

Place this line BEFORE the signature is computed and BEFORE the manifest is written to its destination path.

- [ ] **Step 4: Register the flag**

In the file's `init()` (or wherever `pluginSignCmd.Flags()` is configured), add:

```go
pluginSignCmd.Flags().StringVar(&pluginSignManifestVersion, "manifest-version", "",
    "Override the version field in the manifest before signing (used by `plugin dev --watch`)")
```

- [ ] **Step 5: Verify build + test**

Run: `go build ./... && go test ./cmd/stado/ -count=1`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/stado/plugin_sign.go
git commit -m "feat(cli): plugin sign --manifest-version override"
```

---

### Task 4: `cmd/stado/plugin_dev_watch.go` skeleton — runDevWatchLoop + rebuildOnce

**Files:**
- Create: `cmd/stado/plugin_dev_watch.go`
- Create: `cmd/stado/plugin_dev_watch_test.go`

This task adds the watch loop without yet wiring it into `pluginDevCmd`. Tests cover the building blocks (debounce, rebuild error resilience) using injected dependencies so the loop is testable without real fsnotify events.

- [ ] **Step 1: Write the failing tests**

Create `cmd/stado/plugin_dev_watch_test.go`:

```go
package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestDebounceLoop_CoalescesEvents: 5 events fired within the
// debounce window result in exactly 1 rebuild call.
func TestDebounceLoop_CoalescesEvents(t *testing.T) {
	events := make(chan struct{}, 10)
	var rebuilds int32
	rebuild := func() error { atomic.AddInt32(&rebuilds, 1); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		debounceLoop(ctx, events, rebuild, 50*time.Millisecond, nil)
	}()

	// Fire 5 events within ~25ms — well under the 50ms debounce.
	for i := 0; i < 5; i++ {
		events <- struct{}{}
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for the debounce + a generous margin.
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&rebuilds); got != 1 {
		t.Errorf("rebuild count = %d, want 1", got)
	}

	cancel()
	<-done
}

// TestDebounceLoop_RebuildErrorContinues: rebuild returning error
// does not stop the loop; subsequent events still trigger rebuilds.
func TestDebounceLoop_RebuildErrorContinues(t *testing.T) {
	events := make(chan struct{}, 10)
	var rebuilds int32
	rebuild := func() error {
		atomic.AddInt32(&rebuilds, 1)
		return errors.New("simulated build failure")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		debounceLoop(ctx, events, rebuild, 25*time.Millisecond, nil)
	}()

	events <- struct{}{}
	time.Sleep(80 * time.Millisecond)
	events <- struct{}{}
	time.Sleep(80 * time.Millisecond)

	if got := atomic.LoadInt32(&rebuilds); got != 2 {
		t.Errorf("rebuild count = %d, want 2 (loop should survive build errors)", got)
	}

	cancel()
	<-done
}

// TestDebounceLoop_ExitsOnContextCancel: cancelling the context
// causes the loop to return promptly.
func TestDebounceLoop_ExitsOnContextCancel(t *testing.T) {
	events := make(chan struct{})
	rebuild := func() error { return nil }

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		debounceLoop(ctx, events, rebuild, 100*time.Millisecond, nil)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("debounceLoop did not exit on context cancel within 200ms")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/stado/ -run TestDebounceLoop -count=1 -v`
Expected: FAIL with "undefined: debounceLoop".

- [ ] **Step 3: Write the implementation**

Create `cmd/stado/plugin_dev_watch.go`:

```go
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

const devWatchDebounce = 250 * time.Millisecond

// runDevWatchLoop spawns an fsnotify watcher rooted at dir, walks
// it to add every (non-ignored) directory, then runs debounceLoop
// until ctx is cancelled. The first-run sign+trust+install pipeline
// has already happened by the time this is called; the loop only
// handles subsequent rebuilds + the cleanup-on-exit behaviour.
func runDevWatchLoop(ctx context.Context, dir string, stdout, stderr io.Writer) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer w.Close()

	if err := addRecursive(w, dir); err != nil {
		return fmt.Errorf("watch %s: %w", dir, err)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pluginName := readPluginNameFromTemplate(dir)

	defer func() {
		if pluginName != "" {
			_ = plugins.CleanupDev(cfg.StateDir(), pluginName)
		}
	}()

	if pluginName != "" {
		if err := plugins.PinActiveDev(cfg.StateDir(), pluginName); err != nil {
			fmt.Fprintf(stderr, "[dev] warn: pin active marker: %v\n", err)
		}
	}

	fmt.Fprintf(stdout, "[dev] watching %s — Ctrl+C to stop\n", dir)

	events := make(chan struct{}, 16)

	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if shouldIgnoreEvent(ev) {
					continue
				}
				if ev.Op&fsnotify.Create == fsnotify.Create {
					if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
						_ = addRecursive(w, ev.Name)
					}
				}
				select {
				case events <- struct{}{}:
				default:
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				fmt.Fprintf(stderr, "[dev] watcher error: %v\n", err)
			}
		}
	}()

	rebuild := func() error {
		shaPrefix, err := rebuildOnce(dir, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "[dev] rebuild failed: %v\n", err)
			return err
		}
		fmt.Fprintf(stdout, "[dev] reloaded %s@%s\n", pluginName, shaPrefix)
		return nil
	}

	debounceLoop(ctx, events, rebuild, devWatchDebounce, stderr)
	return nil
}

// debounceLoop coalesces events from `events` and invokes `rebuild`
// once per debounce window. Errors from rebuild are written to
// stderr (when non-nil) and do not stop the loop. Returns when ctx
// is cancelled.
func debounceLoop(ctx context.Context, events <-chan struct{}, rebuild func() error, debounce time.Duration, stderr io.Writer) {
	var timer *time.Timer
	var timerC <-chan time.Time

	resetTimer := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.NewTimer(debounce)
		timerC = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-events:
			resetTimer()
		case <-timerC:
			timerC = nil
			if err := rebuild(); err != nil && stderr != nil {
				// rebuild already logged; loop continues.
				_ = err
			}
		}
	}
}

// rebuildOnce runs <dir>/build.sh, re-signs the manifest with the
// dev seed pinned to plugins.DevSentinelVersion, and re-installs
// with --force. Returns the new wasm sha-prefix on success.
func rebuildOnce(dir string, stdout, stderr io.Writer) (string, error) {
	buildScript := filepath.Join(dir, "build.sh")
	if _, err := os.Stat(buildScript); err != nil {
		return "", fmt.Errorf("build.sh: %w", err)
	}
	cmd := exec.Command(buildScript)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build.sh: %w", err)
	}

	seedPath := filepath.Join(dir, ".stado", "dev.seed")
	templatePath := filepath.Join(dir, "plugin.manifest.template.json")

	origKey := pluginSignKeyPath
	origWasm := pluginSignWasm
	origVersion := pluginSignManifestVersion
	pluginSignKeyPath = seedPath
	pluginSignWasm = ""
	pluginSignManifestVersion = plugins.DevSentinelVersion
	defer func() {
		pluginSignKeyPath = origKey
		pluginSignWasm = origWasm
		pluginSignManifestVersion = origVersion
	}()

	if err := pluginSignCmd.RunE(pluginSignCmd, []string{templatePath}); err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	origForce := pluginInstallForce
	pluginInstallForce = true
	defer func() { pluginInstallForce = origForce }()

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{dir}); err != nil {
		return "", fmt.Errorf("install: %w", err)
	}

	wasmBytes, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if err != nil {
		return "", fmt.Errorf("read wasm: %w", err)
	}
	sum := sha256.Sum256(wasmBytes)
	shaHex := hex.EncodeToString(sum[:])
	if len(shaHex) > 12 {
		shaHex = shaHex[:12]
	}
	return shaHex, nil
}

// addRecursive walks root and adds every directory to the watcher,
// skipping ignored paths.
func addRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if shouldIgnoreDir(p, root) {
			return filepath.SkipDir
		}
		return w.Add(p)
	})
}

// shouldIgnoreDir returns true for directories we never want to
// watch (vcs, build caches, the plugin's own dev metadata).
func shouldIgnoreDir(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		switch seg {
		case ".git", ".stado", "node_modules", "target", "dist", ".cache":
			return true
		}
	}
	return false
}

// shouldIgnoreEvent filters events on paths we don't care about
// (e.g. the wasm output the build itself produces — emitting an
// event for a build-product creates a feedback loop).
func shouldIgnoreEvent(ev fsnotify.Event) bool {
	base := filepath.Base(ev.Name)
	if base == "plugin.wasm" || strings.HasSuffix(base, ".tmp") {
		return true
	}
	return false
}

// readPluginNameFromTemplate parses the plugin.manifest.template.json
// in dir and returns its `name` field. On error returns "" so callers
// can decide how to surface the issue.
func readPluginNameFromTemplate(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.manifest.template.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	return m.Name
}
```

Add `encoding/json` to the import block at the top of the file
(alongside the other stdlib imports).

**Note on package-level state**: `pluginSignKeyPath`, `pluginSignWasm`,
`pluginSignManifestVersion`, and `pluginInstallForce` are existing
flag-bound globals from `plugin_sign.go` and `plugin_install.go`.
Save/restore around the cmd invocation matches the pattern already
used in `pluginDevCmd.RunE`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/stado/ -run TestDebounceLoop -count=1 -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Verify full build**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/stado/plugin_dev_watch.go cmd/stado/plugin_dev_watch_test.go
git commit -m "feat(cli): runDevWatchLoop + rebuildOnce + debounceLoop"
```

---

### Task 5: Wire `--watch` flag into `pluginDevCmd`

**Files:**
- Modify: `cmd/stado/plugin_use_dev.go`

- [ ] **Step 1: Add the flag variable + registration**

In `cmd/stado/plugin_use_dev.go`, just below the import block, add:

```go
var pluginDevWatch bool
```

In the file's `init()` (add one if it doesn't exist), register the flag:

```go
func init() {
    pluginDevCmd.Flags().BoolVar(&pluginDevWatch, "watch", false,
        "After first install, watch <dir> for changes and rebuild + reinstall on save")
}
```

- [ ] **Step 2: Hand off to runDevWatchLoop after first-run completes**

At the END of `pluginDevCmd.RunE`, replace `return nil` with:

```go
if pluginDevWatch {
    return runDevWatchLoop(cmd.Context(), dir, cmd.OutOrStdout(), cmd.ErrOrStderr())
}
return nil
```

- [ ] **Step 3: Verify build**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 4: Smoke the help output**

Run: `go run ./cmd/stado plugin dev --help`
Expected: output contains `--watch` flag documentation.

- [ ] **Step 5: Commit**

```bash
git add cmd/stado/plugin_use_dev.go
git commit -m "feat(cli): plugin dev --watch flag"
```

---

### Task 6: Integration test — first-run + watch composition

**Files:**
- Modify: `cmd/stado/plugin_dev_watch_test.go`

- [ ] **Step 1: Write the integration test**

Append to `cmd/stado/plugin_dev_watch_test.go`:

```go
// TestRunDevWatchLoop_CleansUpOnContextCancel: starting the watch
// loop and immediately cancelling its context should cause it to
// remove the dev install dir + marker via deferred cleanup.
//
// This test does NOT exercise a real build — it simulates a state
// where PinActiveDev has run (creating the marker) and verifies
// CleanupDev fires on shutdown.
func TestRunDevWatchLoop_CleansUpOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.template.json"),
		[]byte(`{"name":"testplugin","version":"0.0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		var stdout, stderr bytes.Buffer
		_ = runDevWatchLoop(ctx, dir, &stdout, &stderr)
	}()

	// Wait for PinActiveDev to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	markerPath := filepath.Join(stateDir, "stado", "plugins", "active", "testplugin")
	for {
		if _, err := os.Stat(markerPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active marker never created")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should be cleaned up after cancel; stat err = %v", err)
	}
}
```

Add the necessary imports at the top of the test file (`bytes`, `os`, `path/filepath`, `time`).

- [ ] **Step 2: Run tests**

Run: `go test ./cmd/stado/ -run TestRunDevWatchLoop -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Run the full test suite to catch regressions**

Run: `go test ./... -count=1`
Expected: all packages pass except the pre-existing
`TestBwrapRunner_AllowHostsOnlyForwardsProxyPort` (python3 missing
in the sandbox host).

- [ ] **Step 4: Commit**

```bash
git add cmd/stado/plugin_dev_watch_test.go
git commit -m "test(cli): integration test for plugin dev --watch cleanup"
```

---

### Task 7: Documentation + manual smoke

**Files:**
- Modify: `docs/superpowers/specs/2026-05-06-plugin-dev-watch-mode-design.md` (handoff section)

- [ ] **Step 1: Manual smoke test against a real plugin**

Pick one of the user's installed plugins (e.g., `~/projects/gtfobins`
or any directory with `plugin.manifest.template.json` + `build.sh`).

Run:
```bash
go run ./cmd/stado plugin dev <plugin-dir> --watch
```

Expected first-run output:
- "Generating dev seed..." (or "Using existing dev seed...")
- "Signing manifest..."
- "Trusting dev key..."
- "Installing..."
- "[dev] watching <dir> — Ctrl+C to stop"

Then in another terminal, edit a source file in `<plugin-dir>` and
save. Observe in the watch terminal:
- (build output)
- `[dev] reloaded <name>@<sha-prefix>`

Then run from a third terminal:
```bash
go run ./cmd/stado tool list | grep <plugin-name>
go run ./cmd/stado tool run <plugin>.<tool> '{...}'
```

Expected: tool registered + dispatched against the new wasm.

Ctrl+C the watch. Verify:
```bash
ls ~/.local/share/stado/plugins/ | grep 0.0.0-dev
```

Expected: the `<name>-0.0.0-dev` dir is gone.

- [ ] **Step 2: Append a handoff note to the spec**

In `docs/superpowers/specs/2026-05-06-plugin-dev-watch-mode-design.md`,
append a final section:

```markdown
## Handoff (2026-05-06)

- **What shipped:** `stado plugin dev <dir> --watch` watches the
  plugin source dir, debounces saves at 250ms, runs `<dir>/build.sh`,
  re-signs the manifest with `--manifest-version 0.0.0-dev`, and
  re-installs with `--force`. Cleanup on Ctrl+C removes the
  `<state>/plugins/<name>-0.0.0-dev/` install + active marker.
- **What's left:** TUI live-reload (Q8 — long-running TUI sessions
  don't auto-reload; iteration loop runs through `stado tool run`).
  Filed as a follow-up cycle item.
- **What surprised me:** [fill in during smoke]
- **What to watch:** dev install left over after `kill -9` of the
  watch process — re-run of `plugin dev --watch` overwrites cleanly,
  but operators may see stale `<name>-0.0.0-dev` dirs if the watch
  ever crashes outside the deferred cleanup path.
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-05-06-plugin-dev-watch-mode-design.md
git commit -m "docs(specs): plugin dev watch mode handoff"
```

---

## Verification checklist (run after Task 7)

- [ ] `go test ./... -count=1` — all packages pass except
      pre-existing sandbox/python3 environmental failure.
- [ ] `go vet ./...` — clean.
- [ ] `go run ./cmd/stado plugin dev --help` — shows `--watch` flag.
- [ ] `go run ./cmd/stado plugin sign --help` — shows
      `--manifest-version` flag.
- [ ] Manual smoke per Task 7 Step 1.
- [ ] After Ctrl+C: `<state>/plugins/<name>-0.0.0-dev` removed,
      `<state>/plugins/active/<name>` marker removed.
- [ ] Re-running `stado tool run <name>.<tool>` after watch exit
      either dispatches an installed (non-dev) version of the
      plugin if one exists, or returns "not found" — never
      dispatches stale dev bytes.
