package runtime

import (
	"context"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerSessionImports(builder wazero.HostModuleBuilder, host *Host) {
	registerSessionReadImport(builder, host)
	registerSessionObserveImport(builder, host)
	registerSessionForkImport(builder, host)
}

func registerSessionReadImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_session_read(field_ptr, field_len, buf_ptr, buf_cap) → int32
	//
	// Phase 7.1b — session:read capability. Copies the named session
	// field's serialised payload into the plugin's buffer. Fields are
	// stringly-typed because the set is small and stable:
	//   "message_count"   → decimal-ASCII integer
	//   "token_count"     → decimal-ASCII integer (input-tokens, current turn)
	//   "session_id"      → session ID string
	//   "last_turn_ref"   → turn tag ref, e.g. "refs/sessions/<id>/turns/5"
	//   "history"         → JSON array of {role,text} objects for the full
	//                       conversation. Largest payload — plugins that
	//                       only need counts should prefer the numeric
	//                       fields above.
	//
	// Returns bytes written, -1 on deny / no-session / unknown-field /
	// truncation beyond buf_cap.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if !host.SessionRead {
				host.Logger.Warn("stado_session_read denied — manifest lacks session:read")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.SessionBridge == nil {
				host.Logger.Warn("stado_session_read: no SessionBridge wired (run context has no session)")
				stack[0] = api.EncodeI32(-1)
				return
			}
			fieldPtr := api.DecodeU32(stack[0])
			fieldLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufCap := api.DecodeU32(stack[3])
			field, err := readString(mod, fieldPtr, fieldLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := host.SessionBridge.ReadField(field)
			if err != nil {
				host.Logger.Warn("stado_session_read failed",
					slog.String("field", field), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if byteLenExceedsCap(data, bufCap) {
				// Don't silently truncate session data — a plugin that
				// asks for "history" and gets half of it would produce
				// nonsense. Signal error; plugin can re-request with a
				// bigger buffer or a smaller field.
				host.Logger.Warn("stado_session_read truncation",
					slog.String("field", field),
					slog.Int("data_bytes", len(data)),
					slog.Uint64("buf_cap", uint64(bufCap)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_session_read")
}

func registerSessionObserveImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_session_next_event(buf_ptr, buf_cap) → int32
	//
	// Phase 7.1b — session:observe capability. Polling variant of the
	// spec's stado_session_observe(callback_ref). WASM has no native
	// closure type, so we expose a non-blocking reader: plugin calls
	// this once per scheduling tick; 0 = no event available right
	// now (plugin should yield), >0 = JSON event payload written,
	// -1 = capability denied or session gone.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if !host.SessionObserve {
				host.Logger.Warn("stado_session_next_event denied — manifest lacks session:observe")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.SessionBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			bufPtr := api.DecodeU32(stack[0])
			bufCap := api.DecodeU32(stack[1])
			ev, err := host.SessionBridge.NextEvent(ctx)
			if err != nil {
				host.Logger.Warn("stado_session_next_event failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if len(ev) == 0 {
				stack[0] = api.EncodeI32(0)
				return
			}
			if byteLenExceedsCap(ev, bufCap) {
				// Oversize event — surface as truncation-denied so the
				// plugin can retry with a bigger buffer rather than
				// receive half an event.
				host.Logger.Warn("stado_session_next_event event larger than buf_cap",
					slog.Int("event_bytes", len(ev)),
					slog.Uint64("buf_cap", uint64(bufCap)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, ev))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_session_next_event")
}

func registerSessionForkImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_session_fork(at_turn_ptr, at_turn_len, seed_ptr, seed_len,
	//                    out_ptr, out_cap) → int32
	//
	// Phase 7.1b — session:fork capability. DESIGN invariant: plugins
	// recover context by forking to a new session, never by rewriting
	// the parent. Returns bytes of the new session ID written to
	// out_ptr, or -1 on deny / fork failure.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			if !host.SessionFork {
				host.Logger.Warn("stado_session_fork denied — manifest lacks session:fork")
				stack[0] = api.EncodeI32(-1)
				return
			}
			if host.SessionBridge == nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			atPtr := api.DecodeU32(stack[0])
			atLen := api.DecodeU32(stack[1])
			seedPtr := api.DecodeU32(stack[2])
			seedLen := api.DecodeU32(stack[3])
			outPtr := api.DecodeU32(stack[4])
			outCap := api.DecodeU32(stack[5])
			atRef, err := readString(mod, atPtr, atLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			seed, err := readString(mod, seedPtr, seedLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			newID, err := host.SessionBridge.Fork(ctx, atRef, seed)
			if err != nil {
				host.Logger.Warn("stado_session_fork failed",
					slog.String("at", atRef), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			data := []byte(newID)
			if byteLenExceedsCap(data, outCap) {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_session_fork")
}
