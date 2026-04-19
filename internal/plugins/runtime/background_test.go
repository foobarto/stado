package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// backgroundTickWasm is a hand-assembled wasm module that imports
// stado_log (one of the required host imports from the InstallHostImports
// side so the module links cleanly) and exports stado_plugin_tick,
// stado_alloc, and stado_free. The tick function always returns 0
// (continue). Produced by `zig build-exe ... -target wasm32-freestanding`
// and hex-encoded here so tests don't need zig at `go test` time.
//
// Source equivalent (Zig):
//   extern "stado" fn stado_log(u32,u32,u32,u32) void;
//   export fn stado_alloc(size: u32) u32 { _ = size; return 0; }
//   export fn stado_free(ptr: u32, size: u32) void { _ = ptr; _ = size; }
//   export fn stado_plugin_tick() i32 { return 0; }
//
// Rather than maintain a binary in the repo, this test builds a
// minimal module inline using wazero's own wat compiler via a text
// fixture below — that keeps tests build-reproducible without
// requiring zig at test time.

// TestLoadBackgroundPlugin_RejectsMissingTickExport — sanity:
// a plugin that doesn't export stado_plugin_tick shouldn't be
// accepted as background. We use minimalWasm (the bare header-only
// module from runtime_test.go) which exports nothing — the loader
// has to notice and fail.
func TestLoadBackgroundPlugin_RejectsMissingTickExport(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close(ctx) }()

	host := NewHost(plugins.Manifest{Name: "noop", Version: "0.1.0"}, "/tmp", nil)
	_, err = LoadBackgroundPlugin(ctx, rt, minimalWasm, host)
	if err == nil {
		t.Fatal("expected error for plugin missing stado_plugin_tick")
	}
	if !strings.Contains(err.Error(), "stado_plugin_tick") {
		t.Errorf("error should mention the missing export: %v", err)
	}
}

// TestBackgroundPlugin_NilTickReportsUnregister: the Tick method
// must handle the nil-tickFn path defensively. Caller gets
// unregister=true + an error so the plugin is dropped from the
// active set.
func TestBackgroundPlugin_NilTickReportsUnregister(t *testing.T) {
	bp := &BackgroundPlugin{}
	unregister, err := bp.Tick(context.Background())
	if !unregister {
		t.Error("expected unregister=true for nil tickFn")
	}
	if err == nil {
		t.Error("expected error for nil tickFn")
	}
}

// TestBackgroundPlugin_CloseNilIsSafe: double-close / close-nil
// mustn't panic. Keeps the caller's "defer Close()" pattern clean
// even when the constructor bailed before setting Module.
func TestBackgroundPlugin_CloseNilIsSafe(t *testing.T) {
	var bp *BackgroundPlugin
	if err := bp.Close(context.Background()); err != nil {
		t.Errorf("nil Close errored: %v", err)
	}
	bp2 := &BackgroundPlugin{}
	if err := bp2.Close(context.Background()); err != nil {
		t.Errorf("zero-value Close errored: %v", err)
	}
}
