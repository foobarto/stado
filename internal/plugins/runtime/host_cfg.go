package runtime

import (
	"context"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// maxPluginRuntimeCfgValueBytes caps the response size of any
// `cfg:*` host import. Config values are paths and small identifiers,
// not arbitrary content; 4 KiB is roomy for anything reasonable while
// preventing a tiny-buffer plugin from triggering very large allocations
// host-side.
const maxPluginRuntimeCfgValueBytes = 4096

// registerCfgImports wires the `cfg:*` capability vocabulary's host
// imports onto the wasm module builder. Each `cfg:<name>` capability
// maps to one host import returning the named string. EP-0029.
//
// All `cfg:*` imports follow the same shape:
//
//	stado_cfg_<name>(buf_ptr, buf_cap) → int32
//	  → returns bytes written to buf, -1 on capability deny / oversize
//
// Returning the value as a write-into-caller-buffer keeps the wasm
// allocation entirely on the plugin side (consistent with stado_log
// and stado_fs_read).
//
// Refusal mode: when the capability is not declared, the import is
// not registered. The plugin's `//go:wasmimport stado stado_cfg_<name>`
// will fail at link time, surfacing as a clean instantiation error
// rather than a silent runtime -1.
func registerCfgImports(builder wazero.HostModuleBuilder, host *Host) {
	if host.CfgStateDir {
		registerCfgStateDirImport(builder, host)
	}
}

func registerCfgStateDirImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_cfg_state_dir(buf_ptr, buf_cap) → int32
	//
	// Writes the operator's stado state-dir path
	// (`$XDG_DATA_HOME/stado/` or fallback) to the caller's buffer.
	// Returns bytes written, or -1 when the value exceeds buf_cap
	// (the plugin should retry with a bigger buffer; in practice
	// state-dir paths are well under 1 KiB).
	//
	// When host.StateDir is empty (caller didn't populate it; rare,
	// usually means the plugin is being run outside a normal
	// command flow), returns 0 — plugin sees an empty string and
	// can fall back to whatever degraded path it has.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			bufPtr := api.DecodeU32(stack[0])
			bufCap := api.DecodeU32(stack[1])
			value := host.StateDir
			if uint32(len(value)) > bufCap {
				host.Logger.Warn("stado_cfg_state_dir truncation",
					slog.Int("value_bytes", len(value)),
					slog.Uint64("buf_cap", uint64(bufCap)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if uint32(len(value)) > maxPluginRuntimeCfgValueBytes {
				host.Logger.Warn("stado_cfg_state_dir denied — value exceeds maxPluginRuntimeCfgValueBytes",
					slog.Int("value_bytes", len(value)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, []byte(value)))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("stado_cfg_state_dir")
}
