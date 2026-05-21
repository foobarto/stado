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
			stack[0] = api.EncodeI32(writeCfgValue(mod, bufPtr, bufCap, host.StateDir, "stado_cfg_state_dir", host.Logger))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("stado_cfg_state_dir")
}

// writeCfgValue implements the shared write-into-caller-buffer contract
// every cfg:* host import follows: it writes value into mod's linear
// memory at [bufPtr, bufPtr+bufCap) and returns the bytes written, or -1
// when value won't fit the caller's buffer or exceeds the
// maxPluginRuntimeCfgValueBytes ceiling. An empty value writes nothing
// and returns 0 (the caller didn't populate the field — the plugin sees
// "" and falls back to whatever degraded path it has). importName names
// the import in the warn logs. Factored out of the import closure so the
// value-flow contract is unit-testable without a guest module that
// imports the symbol.
func writeCfgValue(mod api.Module, bufPtr, bufCap uint32, value, importName string, logger *slog.Logger) int32 {
	if uint32(len(value)) > bufCap {
		logger.Warn(importName+" truncation",
			slog.Int("value_bytes", len(value)),
			slog.Uint64("buf_cap", uint64(bufCap)))
		return -1
	}
	if uint32(len(value)) > maxPluginRuntimeCfgValueBytes {
		logger.Warn(importName+" denied — value exceeds maxPluginRuntimeCfgValueBytes",
			slog.Int("value_bytes", len(value)))
		return -1
	}
	return writeBytes(mod, bufPtr, bufCap, []byte(value))
}
