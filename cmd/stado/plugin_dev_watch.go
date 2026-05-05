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
