package runtime

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerAgentImports(builder wazero.HostModuleBuilder, host *Host) {
	registerAgentSpawnImport(builder, host)
	registerAgentListImport(builder, host)
	registerAgentReadMessagesImport(builder, host)
	registerAgentSendMessageImport(builder, host)
	registerAgentCancelImport(builder, host)
}

// stado_agent_spawn(req_ptr, req_len, result_ptr, result_cap) → int32
// req/result are JSON AgentSpawnRequest / AgentSpawnResult.
func registerAgentSpawnImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			if !host.AgentFleet || host.FleetBridge == nil {
				host.Logger.Warn("stado_agent_spawn: no agent:fleet cap or FleetBridge not wired")
				writeJSONError(mod, resPtr, resCap, "agent:fleet capability required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 64<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req AgentSpawnRequest
			if err := json.Unmarshal(reqBytes, &req); err != nil || req.Prompt == "" {
				writeJSONError(mod, resPtr, resCap, "prompt is required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			result, err := host.FleetBridge.AgentSpawn(ctx, req)
			if err != nil {
				host.Logger.Warn("stado_agent_spawn failed", slog.String("err", err.Error()))
				writeJSONError(mod, resPtr, resCap, err.Error())
				stack[0] = api.EncodeI32(-1)
				return
			}
			payload, _ := json.Marshal(result)
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_agent_spawn")
}

// stado_agent_list(result_ptr, result_cap) → int32
func registerAgentListImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			resPtr, resCap := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])

			if !host.AgentFleet || host.FleetBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			entries, err := host.FleetBridge.AgentList(ctx)
			if err != nil {
				writeJSONError(mod, resPtr, resCap, err.Error())
				stack[0] = api.EncodeI32(-1)
				return
			}
			payload, _ := json.Marshal(entries)
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_agent_list")
}

// stado_agent_read_messages(req_ptr, req_len, result_ptr, result_cap) → int32
// req: JSON {id, since?, timeout_ms?}
func registerAgentReadMessagesImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			if !host.AgentFleet || host.FleetBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				ID        string `json:"id"`
				Since     int    `json:"since"`
				TimeoutMs int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || req.ID == "" {
				writeJSONError(mod, resPtr, resCap, "id is required")
				stack[0] = api.EncodeI32(-1)
				return
			}
			msgs, err := host.FleetBridge.AgentReadMessages(ctx, req.ID, req.Since, req.TimeoutMs)
			if err != nil {
				writeJSONError(mod, resPtr, resCap, err.Error())
				stack[0] = api.EncodeI32(-1)
				return
			}
			payload, _ := json.Marshal(msgs)
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_agent_read_messages")
}

// stado_agent_send_message(req_ptr, req_len) → int32 (0 ok, -1 err)
// req: JSON {id, message}
func registerAgentSendMessageImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])

			if !host.AgentFleet || host.FleetBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 64<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || req.ID == "" {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := host.FleetBridge.AgentSendMessage(ctx, req.ID, req.Message); err != nil {
				host.Logger.Warn("stado_agent_send_message failed",
					slog.String("id", req.ID), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(0)
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_agent_send_message")
}

// stado_agent_cancel(req_ptr, req_len, result_ptr, result_cap) → int32
// req: JSON {id}
func registerAgentCancelImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			if !host.AgentFleet || host.FleetBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || req.ID == "" {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := host.FleetBridge.AgentCancel(ctx, req.ID); err != nil {
				writeJSONError(mod, resPtr, resCap, err.Error())
				stack[0] = api.EncodeI32(-1)
				return
			}
			payload, _ := json.Marshal(map[string]bool{"ok": true})
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_agent_cancel")
}
