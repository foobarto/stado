package runtime

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerLLMImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_llm_invoke(prompt_ptr, prompt_len, out_ptr, out_cap) → int32
	//
	// Phase 7.1b — llm:invoke capability. One-shot completion against
	// the active provider. Budget enforcement: the plugin's manifest
	// declared "llm:invoke:<N>" becomes host.LLMInvokeBudget (default
	// 10000 when no suffix). Tokens consumed across all calls in this
	// instantiation add to host.llmTokensUsed; once the budget is
	// exhausted, further calls return -1 without touching the bridge.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if host.LLMInvokeBudget <= 0 {
				host.Logger.Warn("stado_llm_invoke denied — manifest lacks llm:invoke")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.SessionBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			used := atomic.LoadInt64(&host.llmTokensUsed)
			if used >= int64(host.LLMInvokeBudget) {
				host.Logger.Warn("stado_llm_invoke denied — per-session token budget exhausted",
					slog.Int("budget", host.LLMInvokeBudget),
					slog.Int64("used", used))
				stack[0] = api.EncodeI32(-1)
				return
			}
			promptPtr := api.DecodeU32(stack[0])
			promptLen := api.DecodeU32(stack[1])
			outPtr := api.DecodeU32(stack[2])
			outCap := api.DecodeU32(stack[3])
			prompt, err := readString(mod, promptPtr, promptLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			// Preflight: a single call must not exceed the remaining
			// budget.  We estimate input tokens at ~4 bytes/token (same
			// fallback the bridge uses).  Output is capped by the
			// remaining budget so an oversized response can't silently
			// exhaust the allowance.
			const bytesPerToken = 4
			promptTokEstimate := (len(prompt) + bytesPerToken - 1) / bytesPerToken
			remaining := host.LLMInvokeBudget - int(used)
			if promptTokEstimate >= remaining {
				host.Logger.Warn("stado_llm_invoke denied — call would exceed budget",
					slog.Int("budget", host.LLMInvokeBudget),
					slog.Int64("used", used),
					slog.Int("prompt_estimate", promptTokEstimate))
				stack[0] = api.EncodeI32(-1)
				return
			}
			reply, tokens, err := host.SessionBridge.InvokeLLM(ctx, prompt)
			if err != nil {
				host.Logger.Warn("stado_llm_invoke failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			atomic.AddInt64(&host.llmTokensUsed, int64(tokens))
			data := []byte(reply)
			if uint32(len(data)) > outCap {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_llm_invoke")
}
