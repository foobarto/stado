package runtime

import (
	"context"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerMemoryImports(builder wazero.HostModuleBuilder, host *Host) {
	registerMemoryProposeImport(builder, host)
	registerMemoryQueryImport(builder, host)
	registerMemoryUpdateImport(builder, host)
}

func registerMemoryProposeImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_memory_propose(json_ptr, json_len) -> int32
	//
	// Stores a candidate memory for later user review. Returns 0 on
	// success, -1 on capability denial, invalid JSON, or unavailable
	// memory storage.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if !host.MemoryPropose {
				host.Logger.Warn("stado_memory_propose denied — manifest lacks memory:propose")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.MemoryBridge == nil {
				host.Logger.Warn("stado_memory_propose: no MemoryBridge wired")
				stack[0] = api.EncodeI32(-1)
				return
			}
			ptr := api.DecodeU32(stack[0])
			length := api.DecodeU32(stack[1])
			payload, err := readBytes(mod, ptr, length)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := host.MemoryBridge.Propose(ctx, payload); err != nil {
				host.Logger.Warn("stado_memory_propose failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("stado_memory_propose")
}

func registerMemoryQueryImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_memory_query(json_ptr, json_len, buf_ptr, buf_cap) -> int32
	//
	// Reads approved memories matching the query JSON. Returns bytes
	// written, or -1 on denial/error/truncation.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if !host.MemoryRead {
				host.Logger.Warn("stado_memory_query denied — manifest lacks memory:read")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.MemoryBridge == nil {
				host.Logger.Warn("stado_memory_query: no MemoryBridge wired")
				stack[0] = api.EncodeI32(-1)
				return
			}
			queryPtr := api.DecodeU32(stack[0])
			queryLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufCap := api.DecodeU32(stack[3])
			payload, err := readBytes(mod, queryPtr, queryLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			result, err := host.MemoryBridge.Query(ctx, payload)
			if err != nil {
				host.Logger.Warn("stado_memory_query failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if byteLenExceedsCap(result, bufCap) {
				host.Logger.Warn("stado_memory_query result larger than buf_cap",
					slog.Int("result_bytes", len(result)),
					slog.Uint64("buf_cap", uint64(bufCap)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, result))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_memory_query")
}

func registerMemoryUpdateImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_memory_update(json_ptr, json_len) -> int32
	//
	// Applies a user-approved memory mutation. Returns 0 on success,
	// -1 on capability denial or mutation failure.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if !host.MemoryWrite {
				host.Logger.Warn("stado_memory_update denied — manifest lacks memory:write")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.MemoryBridge == nil {
				host.Logger.Warn("stado_memory_update: no MemoryBridge wired")
				stack[0] = api.EncodeI32(-1)
				return
			}
			ptr := api.DecodeU32(stack[0])
			length := api.DecodeU32(stack[1])
			payload, err := readBytes(mod, ptr, length)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := host.MemoryBridge.Update(ctx, payload); err != nil {
				host.Logger.Warn("stado_memory_update failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("stado_memory_update")
}
