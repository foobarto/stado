package runtime

// Persistent plugin lifecycle — the "background plugin" shape.
// Plugins that want to observe a session over its whole lifetime
// (auto-compaction, telemetry bridges, session recorders) export
// `stado_plugin_tick() → i32` in addition to the usual ABI. The
// host keeps the instantiated module alive and calls Tick once per
// turn boundary; the plugin polls stado_session_next_event to see
// what happened, acts if it wants to, and returns.
//
// Return codes from Tick:
//   0  → continue; call again next turn
//  !0  → unregister; close the module and stop ticking
//
// The lifecycle is load-once, tick-many, close-once — separate from
// the one-shot PluginTool pattern (which throws away its runtime
// after a single invocation).

import (
	"context"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/plugins"
)

// BackgroundPlugin is a persistent plugin instance that ticks on
// every turn boundary. One instance per session; the host owns it
// and calls Tick / Close.
type BackgroundPlugin struct {
	Module   *Module
	Host     *Host
	Manifest plugins.Manifest
	tickFn   api.Function // stado_plugin_tick export, resolved at load
}

// LoadBackgroundPlugin compiles wasmBytes under rt, installs the
// host imports against host, and resolves stado_plugin_tick. Returns
// an error when the plugin doesn't export tick — not every plugin
// should be loaded as background, and accidentally promoting a
// one-shot plugin here would just waste a wazero instance per
// turn boundary.
func LoadBackgroundPlugin(ctx context.Context, rt *Runtime, wasmBytes []byte, host *Host) (*BackgroundPlugin, error) {
	if err := InstallHostImports(ctx, rt, host); err != nil {
		return nil, fmt.Errorf("background: host imports: %w", err)
	}
	mod, err := rt.Instantiate(ctx, wasmBytes, host.Manifest)
	if err != nil {
		return nil, fmt.Errorf("background: instantiate: %w", err)
	}
	tick := mod.wasmMod.ExportedFunction("stado_plugin_tick")
	if tick == nil {
		_ = mod.Close(ctx)
		return nil, errors.New("background: plugin does not export stado_plugin_tick — not a background plugin")
	}
	return &BackgroundPlugin{
		Module:   mod,
		Host:     host,
		Manifest: host.Manifest,
		tickFn:   tick,
	}, nil
}

// Tick invokes the plugin's stado_plugin_tick export. A non-zero
// return value signals "unregister me" — the caller should Close the
// plugin and drop it from the active set. Wazero traps (which manifest
// as errors here) also unregister on the theory that a plugin that
// panics is better muted than repeatedly-failing.
//
// Tick is safe to call concurrently with Close, but not with other
// ticks on the same BackgroundPlugin (wasm modules aren't reentrant
// within an instance). Callers typically run one Tick at a time per
// plugin.
func (bp *BackgroundPlugin) Tick(ctx context.Context) (unregister bool, err error) {
	if bp == nil || bp.tickFn == nil {
		return true, errors.New("background: plugin not loaded")
	}
	ret, err := bp.tickFn.Call(ctx)
	if err != nil {
		return true, fmt.Errorf("background: tick trapped: %w", err)
	}
	if len(ret) == 0 {
		return false, nil
	}
	code := api.DecodeI32(ret[0])
	return code != 0, nil
}

// Close tears down the wazero module for this plugin. Idempotent.
func (bp *BackgroundPlugin) Close(ctx context.Context) error {
	if bp == nil || bp.Module == nil {
		return nil
	}
	return bp.Module.Close(ctx)
}

// Name + Version pass through to the loaded manifest — useful for
// logging which plugin ticked.
func (bp *BackgroundPlugin) Name() string    { return bp.Manifest.Name }
func (bp *BackgroundPlugin) Version() string { return bp.Manifest.Version }
