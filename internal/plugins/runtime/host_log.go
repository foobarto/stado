package runtime

import (
	"context"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerLogImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_log(level_ptr, level_len, msg_ptr, msg_len)
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			levelPtr := api.DecodeU32(stack[0])
			levelLen := api.DecodeU32(stack[1])
			msgPtr := api.DecodeU32(stack[2])
			msgLen := api.DecodeU32(stack[3])
			level, err := readStringLimited(mod, levelPtr, levelLen, maxPluginRuntimeLogLevelBytes)
			if err != nil {
				return
			}
			msg, err := readStringLimited(mod, msgPtr, msgLen, maxPluginRuntimeLogMessageBytes)
			if err != nil {
				return
			}
			switch strings.ToLower(level) {
			case "debug":
				host.Logger.Debug(msg)
			case "warn", "warning":
				host.Logger.Warn(msg)
			case "error":
				host.Logger.Error(msg)
			default:
				host.Logger.Info(msg)
			}
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, nil).
		Export("stado_log")
}
