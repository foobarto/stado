package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// llmInvokeArgs is the JSON envelope stado_llm_invoke now accepts.
// Was a bare-prompt + 4 i32s ABI; now the first ptr/len pair carries
// JSON so we can extend per-call options without reshuffling the
// signature. EP-0038i (personas integration).
type llmInvokeArgs struct {
	Prompt      string  `json:"prompt"`
	Persona     string  `json:"persona,omitempty"`
	Model       string  `json:"model,omitempty"`
	System      string  `json:"system,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

func registerLLMImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_llm_invoke(args_ptr, args_len, out_ptr, out_cap) → int32
	//
	// args is JSON: {prompt, persona?, model?, system?, max_tokens?, temperature?}.
	// One-shot completion against the active provider. Budget
	// enforcement: the plugin's manifest declared "llm:invoke:<N>"
	// becomes host.LLMInvokeBudget (default 10000 when no suffix).
	// Tokens consumed across all calls in this instantiation add to
	// host.llmTokensUsed; once exhausted, further calls return -1.
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
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			outPtr := api.DecodeU32(stack[2])
			outCap := api.DecodeU32(stack[3])
			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, maxPluginRuntimeLLMPromptBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var args llmInvokeArgs
			if err := json.Unmarshal(argsBytes, &args); err != nil || args.Prompt == "" {
				host.Logger.Warn("stado_llm_invoke: malformed args", slog.String("err", fmt.Sprintf("%v", err)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			// Preflight: a single call must not exceed the remaining
			// budget. We estimate input tokens at ~4 bytes/token (same
			// fallback the bridge uses). Output is capped by the
			// remaining budget so an oversized response can't silently
			// exhaust the allowance.
			const bytesPerToken = 4
			promptTokEstimate := (len(args.Prompt) + bytesPerToken - 1) / bytesPerToken
			remaining := host.LLMInvokeBudget - int(used)
			if promptTokEstimate >= remaining {
				host.Logger.Warn("stado_llm_invoke denied — call would exceed budget",
					slog.Int("budget", host.LLMInvokeBudget),
					slog.Int64("used", used),
					slog.Int("prompt_estimate", promptTokEstimate))
				stack[0] = api.EncodeI32(-1)
				return
			}
			invokeOpts := LLMInvokeOpts{
				Persona:     args.Persona,
				Model:       args.Model,
				System:      args.System,
				MaxTokens:   args.MaxTokens,
				Temperature: args.Temperature,
			}
			reply, tokens, err := host.SessionBridge.InvokeLLM(ctx, args.Prompt, invokeOpts)
			if err != nil {
				host.Logger.Warn("stado_llm_invoke failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			atomic.AddInt64(&host.llmTokensUsed, int64(tokens))
			data := []byte(reply)
			if byteLenExceedsCap(data, outCap) {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_llm_invoke")
}
