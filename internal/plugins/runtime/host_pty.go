package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

const (
	maxPluginRuntimePTYInputBytes  uint32 = 64 << 10
	maxPluginRuntimePTYWriteBytes  uint32 = 1 << 20
	maxPluginRuntimePTYReadCap     uint32 = 4 << 20
	pluginRuntimePTYDefaultTimeout        = 100 * time.Millisecond
)

// registerPTYImports wires nine host imports for the PTY plugin
// surface: create / list / attach / detach / write / read / signal /
// resize / destroy. All are gated on the "exec:pty" capability — if
// the manifest doesn't declare it, none are exported and link-time
// resolution from the wasm side fails (loud, not silent).
//
// Wire format: every import takes (args_ptr, args_len, result_ptr,
// result_cap) with args being JSON. On success the import either
// writes JSON to result and returns the byte-count, or returns a
// positive plain integer when the result is a single number (id,
// bytes-written, etc — see per-import comments). On error the import
// writes the error string to result and returns -byte_count, mirroring
// the encodeToolSidePayload convention.
func registerPTYImports(builder wazero.HostModuleBuilder, host *Host) {
	if !host.ExecPTY || host.PTYManager == nil {
		return
	}
	registerPTYCreate(builder, host)
	registerPTYList(builder, host)
	registerPTYAttach(builder, host)
	registerPTYDetach(builder, host)
	registerPTYWrite(builder, host)
	registerPTYRead(builder, host)
	registerPTYSignal(builder, host)
	registerPTYResize(builder, host)
	registerPTYDestroy(builder, host)
}

// stado_pty_create(args_ptr, args_len, result_ptr, result_cap) → i64
//
// args = JSON pty.SpawnOpts. Returns the new id on success (always
// >0). On error, writes error string to result and returns -length.
func registerPTYCreate(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])

			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, maxPluginRuntimePTYInputBytes)
			if err != nil {
				stack[0] = api.EncodeI64(int64(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error()))))
				return
			}
			var opts pty.SpawnOpts
			if len(argsBytes) > 0 {
				if err := json.Unmarshal(argsBytes, &opts); err != nil {
					stack[0] = api.EncodeI64(int64(encodeToolSidePayload(mod, resPtr, resCap, []byte("pty: invalid args json: "+err.Error()))))
					return
				}
			}
			id, err := host.PTYManager.Spawn(opts)
			if err != nil {
				host.Logger.Warn("stado_pty_create failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI64(int64(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error()))))
				return
			}
			stack[0] = api.EncodeI64(int64(id))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64}).
		Export("stado_pty_create")
}

// stado_pty_list(buf_ptr, buf_cap) → i32
//
// Writes JSON array of pty.SessionInfo. Returns byte count or -length
// on error.
func registerPTYList(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			bufPtr := api.DecodeU32(stack[0])
			bufCap := api.DecodeU32(stack[1])
			infos := host.PTYManager.List()
			payload, err := json.Marshal(infos)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, bufPtr, bufCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, payload))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_list")
}

// stado_pty_attach(args_ptr, args_len, result_ptr, result_cap) → i32
//
// args = {"id": uint64, "force": bool}. Returns 0 on success, -length
// with error string on failure.
func registerPTYAttach(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID    uint64 `json:"id"`
				Force bool   `json:"force"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if err := host.PTYManager.Attach(req.ID, pty.AttachOpts{Force: req.Force}); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_attach")
}

// stado_pty_detach(args_ptr, args_len, result_ptr, result_cap) → i32
func registerPTYDetach(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID uint64 `json:"id"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if err := host.PTYManager.Detach(req.ID); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_detach")
}

// stado_pty_write(id_lo, id_hi, buf_ptr, buf_len, err_ptr, err_cap) → i32
//
// Returns bytes written, -length with err string on failure. The id
// is split across two i32s because wasm32 host imports cap params at
// i32 unless explicitly declared i64; for Write we want the buffer
// pointer/length pair to stay i32-aligned.
func registerPTYWrite(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			idLo := api.DecodeU32(stack[0])
			idHi := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufLen := api.DecodeU32(stack[3])
			errPtr := api.DecodeU32(stack[4])
			errCap := api.DecodeU32(stack[5])
			id := uint64(idHi)<<32 | uint64(idLo)
			if bufLen > maxPluginRuntimePTYWriteBytes {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, errPtr, errCap, []byte("pty: write payload too large")))
				return
			}
			data, err := readBytes(mod, bufPtr, bufLen)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, errPtr, errCap, []byte(err.Error())))
				return
			}
			n, err := host.PTYManager.Write(id, data)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, errPtr, errCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(int32(n))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_write")
}

// stado_pty_read(id_lo, id_hi, max_bytes, timeout_ms, buf_ptr, buf_cap) → i32
//
// Returns bytes read (positive, may be 0 on timeout-with-no-data), -1
// when the session has closed and the ring is empty (EOF), or
// -length with err string on other failure.
func registerPTYRead(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			idLo := api.DecodeU32(stack[0])
			idHi := api.DecodeU32(stack[1])
			maxBytes := api.DecodeU32(stack[2])
			timeoutMs := api.DecodeU32(stack[3])
			bufPtr := api.DecodeU32(stack[4])
			bufCap := api.DecodeU32(stack[5])
			id := uint64(idHi)<<32 | uint64(idLo)
			if maxBytes == 0 || maxBytes > maxPluginRuntimePTYReadCap {
				maxBytes = maxPluginRuntimePTYReadCap
			}
			if maxBytes > bufCap {
				maxBytes = bufCap
			}
			timeout := time.Duration(timeoutMs) * time.Millisecond
			if timeoutMs == 0 {
				timeout = pluginRuntimePTYDefaultTimeout
			}
			data, err := host.PTYManager.Read(id, int(maxBytes), timeout)
			if err != nil {
				if errors.Is(err, io.EOF) {
					stack[0] = api.EncodeI32(-1)
					return
				}
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, bufPtr, bufCap, []byte(err.Error())))
				return
			}
			if len(data) == 0 {
				stack[0] = api.EncodeI32(0)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_read")
}

// stado_pty_signal(args_ptr, args_len, result_ptr, result_cap) → i32
//
// args = {"id": uint64, "sig": int}. sig is a POSIX signal number
// (e.g. 2 = SIGINT, 15 = SIGTERM). Returns 0 on success.
func registerPTYSignal(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID  uint64 `json:"id"`
				Sig int    `json:"sig"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if err := host.PTYManager.Signal(req.ID, syscall.Signal(req.Sig)); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_signal")
}

// stado_pty_resize(args_ptr, args_len, result_ptr, result_cap) → i32
func registerPTYResize(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID   uint64 `json:"id"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if err := host.PTYManager.Resize(req.ID, req.Cols, req.Rows); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_resize")
}

// stado_pty_destroy(args_ptr, args_len, result_ptr, result_cap) → i32
func registerPTYDestroy(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID uint64 `json:"id"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if err := host.PTYManager.Destroy(req.ID); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_pty_destroy")
}

func decodePTYArgs(mod api.Module, ptr, length uint32, dst any) error {
	argsBytes, err := readBytesLimited(mod, ptr, length, maxPluginRuntimePTYInputBytes)
	if err != nil {
		return err
	}
	if len(argsBytes) == 0 {
		return errors.New("pty: empty args")
	}
	if err := json.Unmarshal(argsBytes, dst); err != nil {
		return errors.New("pty: invalid args json: " + err.Error())
	}
	return nil
}
