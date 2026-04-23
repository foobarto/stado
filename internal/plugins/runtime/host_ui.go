package runtime

import (
	"context"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerUIApprovalImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_ui_approve(title_ptr, title_len, body_ptr, body_len) -> int32
	//
	// Return values:
	//   1  allow
	//   0  deny
	//  -1  unavailable / denied by capability / UI error
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			titlePtr := api.DecodeU32(stack[0])
			titleLen := api.DecodeU32(stack[1])
			bodyPtr := api.DecodeU32(stack[2])
			bodyLen := api.DecodeU32(stack[3])
			title, err := readString(mod, titlePtr, titleLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			body, err := readString(mod, bodyPtr, bodyLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.UIApproval {
				host.Logger.Warn("stado_ui_approve denied — manifest lacks ui:approval")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.ApprovalBridge == nil {
				host.Logger.Warn("stado_ui_approve unavailable — no interactive approval bridge")
				stack[0] = api.EncodeI32(-1)
				return
			}
			allow, err := host.ApprovalBridge.RequestApproval(ctx, title, body)
			if err != nil {
				host.Logger.Warn("stado_ui_approve failed", "err", err)
				stack[0] = api.EncodeI32(-1)
				return
			}
			if allow {
				stack[0] = api.EncodeI32(1)
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("stado_ui_approve")
}
