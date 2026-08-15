package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
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
		debounceLoop(ctx, events, rebuild, 50*time.Millisecond)
	}()

	for i := 0; i < 5; i++ {
		events <- struct{}{}
		time.Sleep(5 * time.Millisecond)
	}

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
		debounceLoop(ctx, events, rebuild, 25*time.Millisecond)
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
		debounceLoop(ctx, events, rebuild, 100*time.Millisecond)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("debounceLoop did not exit on context cancel within 200ms")
	}
}

// TestRunDevWatchLoop_CleansUpOnContextCancel: starting the watch
// loop and immediately cancelling its context should cause it to
// remove the dev install dir + marker via deferred cleanup.
//
// This test does NOT exercise a real build — it simulates the exact
// source-keyed dev install selected by plugin dev's initial install and
// verifies CleanupDev fires on shutdown.
func TestRunDevWatchLoop_CleansUpOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	manifest := plugins.Manifest{
		Name: "testplugin", Version: plugins.DevSentinelVersion,
		WASMSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	canonical, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.template.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pluginsDir := filepath.Join(stateDir, "stado", "plugins")
	record, err := plugins.NewLocalInstallRecord(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	installedDir := filepath.Join(pluginsDir, record.StoreKey)
	if err := os.MkdirAll(installedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"plugin.manifest.json", "plugin.manifest.sig"} {
		data, readErr := os.ReadFile(filepath.Join(dir, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(installedDir, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.WriteInstallRecord(installedDir, record, manifest); err != nil {
		t.Fatal(err)
	}
	pkg := plugins.InstalledPackage{
		Dir: installedDir, Record: record, Manifest: manifest,
		Identity: plugins.RuntimeIdentity{Namespace: record.Namespace},
	}
	if err := plugins.WriteActivePackageMarker(pluginsDir, pkg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		var stdout, stderr bytes.Buffer
		_ = runDevWatchLoop(ctx, dir, &stdout, &stderr)
	}()

	// Wait for the watcher to start while the initial exact marker remains.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if marker, present, _ := plugins.ReadActivePackageStoreKey(pluginsDir, record.Namespace); present && marker == record.StoreKey {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active marker never created")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	if marker, present, err := plugins.ReadActivePackageStoreKey(pluginsDir, record.Namespace); err != nil || present {
		t.Errorf("marker should be cleaned up after cancel; got %q present=%v err=%v", marker, present, err)
	}
	if _, err := os.Stat(installedDir); !os.IsNotExist(err) {
		t.Errorf("dev package should be cleaned up after cancel; stat err = %v", err)
	}
}
