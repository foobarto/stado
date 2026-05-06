// stado_progress — operator-visible progress emit. EP-0038h.
//
// Tools that take more than ~2s today appear silent to the operator
// until the final result lands. Tester #4: a long probe should be
// able to emit "checking host 17/256" so the operator can tell it's
// making progress.
//
// Audience: operator only. The agent / model sees only the final
// tool result; mid-tool partials to the model break tool-call
// atomicity in current LLM contracts and are explicitly out of scope.
//
// Wiring: Host.Progress is a callback the host caller (TUI, headless,
// `stado plugin run`) populates. When nil, the import drops silently
// — the plugin shouldn't fail because the operator surface isn't
// hooked up.
package runtime

import (
	"context"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// maxProgressTextBytes bounds a single progress emission. Bigger
// payloads return -1; plugins can chunk if they really need to.
const maxProgressTextBytes = 4 * 1024

// stado_progress(text_ptr, text_len) → i32
//
// Returns 0 on success or silent-drop (no callback wired). Returns
// -1 only when the input is malformed (overlong, unreadable memory).
func registerProgressImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, textPtr, textLen int32) int32 {
			if textLen < 0 || textLen > maxProgressTextBytes {
				return -1
			}
			if textLen == 0 {
				return emitProgress(host, "")
			}
			text, ok := readMemoryString(mod, uint32(textPtr), uint32(textLen))
			if !ok {
				return -1
			}
			return emitProgress(host, text)
		}).
		Export("stado_progress")
}

// emitProgress is the tested core of stado_progress. Centralised so
// the policy (size cap, nil-callback drop, plugin-name attach) lives
// in one place independent of the wasm bridge.
func emitProgress(host *Host, text string) int32 {
	if len(text) > maxProgressTextBytes {
		return -1
	}
	if text == "" {
		return 0
	}
	if host.Progress != nil {
		host.Progress(host.Manifest.Name, text)
	}
	return 0
}
