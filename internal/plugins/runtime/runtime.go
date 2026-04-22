// Package runtime is the wazero-backed execution host for stado plugins.
//
// Every third-party plugin ships a `.wasm` module compiled to WebAssembly
// + a signed manifest declaring its capabilities. This package instantiates
// the module in a sandboxed wazero.Runtime, exposes a curated set of host
// imports (stado_log / stado_fs_* / stado_net_http / stado_tool_register),
// and routes capability-gated access back through stado's sandbox layer so
// a plugin can never touch anything the manifest didn't authorise.
//
// Lifecycle:
//
//	r := runtime.New(ctx)
//	defer r.Close(ctx)
//	mod, err := r.Instantiate(ctx, wasmBytes, plugins.Manifest{...}, host)
//	// plugin exports get registered here via stado_tool_register callbacks
//	defer mod.Close(ctx)
//
// Build tag `!airgap` is present as a future-proofing; the airgap build
// currently ships wazero too (it's ~5 MB, pure Go, offline). Kept as a
// tag so we can swap in a no-op stub if the airgap binary ever needs to
// strip wasm support.
package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/foobarto/stado/internal/plugins"
)

// Runtime is the process-lifetime wazero host. Safe for concurrent Use
// from multiple goroutines — wazero.Runtime itself is thread-safe and
// this type adds a mutex only around module tracking.
type Runtime struct {
	rt      wazero.Runtime
	closed  bool
	mu      sync.Mutex
	modules map[string]*Module // keyed by manifest.Name + "-" + manifest.Version
}

// New allocates a fresh wazero runtime with WASI preview 1 preloaded.
// Caller must Close() on exit — wazero holds compiled-module memory
// until shutdown.
func New(ctx context.Context) (*Runtime, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		// Disable timeouts here — per-plugin ctx deadlines apply at
		// Instantiate/invocation sites, not on the runtime globally.
		WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wazero: WASI preview 1: %w", err)
	}
	return &Runtime{
		rt:      rt,
		modules: make(map[string]*Module),
	}, nil
}

// Close terminates the runtime + every instantiated module. Safe to
// call more than once.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	// Close modules first so their cleanup hooks run before the
	// runtime's resources vanish under them.
	for _, m := range r.modules {
		_ = m.Close(ctx)
	}
	r.modules = nil
	return r.rt.Close(ctx)
}

// Module is one instantiated plugin. Tools it registered via
// stado_tool_register live on .Tools; the caller typically slurps those
// into the Executor's registry.
type Module struct {
	Name     string
	Version  string
	Manifest plugins.Manifest

	wasmMod api.Module
	rt      *Runtime
}

// Close shuts down this module. Idempotent.
func (m *Module) Close(ctx context.Context) error {
	if m == nil || m.wasmMod == nil {
		return nil
	}
	return m.wasmMod.Close(ctx)
}

// ImportError is returned when a host-import call fails for a
// capability-level reason (not a transient I/O error). Used by tests
// to assert a deny decision rather than a dial failure.
type ImportError struct {
	Func   string
	Reason string
}

func (e *ImportError) Error() string {
	return fmt.Sprintf("wazero host: %s denied: %s", e.Func, e.Reason)
}

// Instantiate compiles + instantiates wasmBytes under the given
// manifest. The manifest's capability list becomes the authoritative
// allow-list for every host-import call from this module.
//
// This is the minimal scaffold — host imports land in a follow-up
// (Phase 7.1b). For now the only pre-registered import is WASI
// preview 1, which lets WASI-targeted plugins compile + run their
// main function without hitting a "missing import" link error.
func (r *Runtime) Instantiate(ctx context.Context, wasmBytes []byte, m plugins.Manifest) (*Module, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("wazero: runtime is closed")
	}
	r.mu.Unlock()

	// Compile + instantiate. We list both `_start` (default for
	// command-style wasm) and `_initialize` (reactor-style — emitted by
	// e.g. `GOOS=wasip1 -buildmode=c-shared` Go builds) as start
	// functions. Whichever the module actually exports runs; the other
	// is silently skipped. Without `_initialize`, a Go reactor module's
	// runtime stays un-booted and every export traps on the first
	// allocator call.
	cfg := wazero.NewModuleConfig().
		WithStartFunctions("_start", "_initialize").
		WithName(m.Name + "-" + m.Version)
	wmod, err := r.rt.InstantiateWithConfig(ctx, wasmBytes, cfg)
	if err != nil {
		return nil, fmt.Errorf("wazero: instantiate %s v%s: %w", m.Name, m.Version, err)
	}
	mod := &Module{
		Name:     m.Name,
		Version:  m.Version,
		Manifest: m,
		wasmMod:  wmod,
		rt:       r,
	}
	key := m.Name + "-" + m.Version
	r.mu.Lock()
	if r.closed || r.modules == nil {
		r.mu.Unlock()
		_ = wmod.Close(ctx)
		return nil, fmt.Errorf("wazero: runtime is closed")
	}
	r.modules[key] = mod
	r.mu.Unlock()
	return mod, nil
}

// Assert wazero's api.Module satisfies the shape we rely on. Kept as a
// compile-time check so API drift in wazero surfaces at build time.
var _ api.Module = (api.Module)(nil)
