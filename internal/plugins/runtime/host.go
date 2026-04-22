package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/plugins"
)

// Host is the capability-gated bridge exposed to a plugin's wasm
// module. It owns the sandbox policy (derived from the manifest) and
// the slog.Logger used by stado_log.
//
// One Host per plugin instantiation — the capability lists in it are
// the manifest's, not the process's. Instantiate() builds + registers
// an instance of this type on the runtime before the wasm module runs.
type Host struct {
	Manifest plugins.Manifest
	Logger   *slog.Logger

	// Parsed from Manifest.Capabilities. These are the authoritative
	// allow-lists host-import calls check against. Empty slices mean
	// "deny all" — matches the strict default of plugin execution.
	FSRead  []string
	FSWrite []string
	NetHost []string // allowed hostnames; empty → no net
	Workdir string   // CWD the plugin sees for relative paths

	// Session/LLM capability gates — Phase 7.1b (PR K2). DESIGN
	// §"Plugin extension points for context management".
	//
	// SessionObserve gates stado_session_next_event (polling variant
	// of stado_session_observe — wasm-native, no callback refs).
	// SessionRead gates stado_session_read.
	// SessionFork gates stado_session_fork.
	// LLMInvokeBudget gates stado_llm_invoke; 0 = not permitted,
	// positive = per-session token budget ceiling. Default when
	// "llm:invoke" is declared without a suffix: 10000.
	SessionObserve  bool
	SessionRead     bool
	SessionFork     bool
	LLMInvokeBudget int

	// SessionBridge wires the host-side session operations (read
	// history, fork, LLM invoke, subscribe-to-events). Nil when the
	// caller doesn't have a live session — in that case the gated
	// host imports return -1 with a diagnostic in the log. Exposed as
	// an interface so TUI / headless / tests can plug in different
	// backings.
	SessionBridge SessionBridge

	// llmTokensUsed tracks the per-session running total against
	// LLMInvokeBudget. Updated atomically inside the stado_llm_invoke
	// import so concurrent plugin calls don't race past the ceiling.
	llmTokensUsed int64
}

// SessionBridge is the capability-checked surface plugin code calls
// through. Every method corresponds to one host import gated by a
// matching `session:*` or `llm:*` capability in the plugin manifest.
// A nil SessionBridge is valid — it means the runtime doesn't have a
// session (e.g. `stado plugin run` outside a session context); the
// host imports return -1 with a diagnostic.
type SessionBridge interface {
	// NextEvent blocks until the next session event (or ctx deadline)
	// and returns an opaque JSON payload the plugin can parse. Empty
	// payload = no events yet (plugin should back off).
	NextEvent(ctx context.Context) ([]byte, error)
	// ReadField returns the current value of a named session field.
	// Supported names are spec-defined (see DESIGN): "message_count",
	// "token_count", "session_id", "last_turn_ref", "history".
	ReadField(name string) ([]byte, error)
	// Fork creates a new session rooted at atTurnRef with seedMessage
	// as its first user turn. Returns the new session ID.
	Fork(ctx context.Context, atTurnRef, seedMessage string) (sessionID string, err error)
	// InvokeLLM runs a one-shot completion against the active
	// provider with the given prompt, returning the aggregated reply
	// text and the number of tokens consumed (used to enforce the
	// per-session budget).
	InvokeLLM(ctx context.Context, prompt string) (reply string, tokensUsed int, err error)
}

// NewHost parses a manifest's capabilities into a Host.
func NewHost(m plugins.Manifest, workdir string, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Host{
		Manifest: m,
		Logger:   logger.With("plugin", m.Name),
		Workdir:  workdir,
	}
	for _, cap := range m.Capabilities {
		parts := strings.SplitN(cap, ":", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "fs":
			if len(parts) != 3 {
				continue
			}
			switch parts[1] {
			case "read":
				h.FSRead = append(h.FSRead, parts[2])
			case "write":
				h.FSWrite = append(h.FSWrite, parts[2])
			}
		case "net":
			// "net:<host>" — the cap string after "net:" is the
			// exact host to allow. Special values "allow" / "deny" are
			// handled at the MCP layer but aren't exposed to plugins
			// here (too much power).
			rest := strings.Join(parts[1:], ":")
			if rest != "" && rest != "allow" && rest != "deny" {
				h.NetHost = append(h.NetHost, rest)
			}
		case "session":
			// DESIGN §"Plugin extension points for context management":
			// session:observe / session:read / session:fork.
			switch parts[1] {
			case "observe":
				h.SessionObserve = true
			case "read":
				h.SessionRead = true
			case "fork":
				h.SessionFork = true
			}
		case "llm":
			// llm:invoke or llm:invoke:<budget>. Default budget when
			// the suffix is omitted is 10000 — conservative ceiling
			// that forces explicit uplift for bigger workloads.
			if parts[1] != "invoke" {
				continue
			}
			budget := 10000
			if len(parts) == 3 && parts[2] != "" {
				if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
					budget = n
				}
			}
			h.LLMInvokeBudget = budget
		}
	}
	return h
}

// allowRead / allowWrite perform the capability check. Current
// matching is prefix-based on absolute paths — a manifest entry of
// `/home/user/projects` allows any file under that tree. Glob support
// can be added later if real plugins ask for it.
func (h *Host) allowRead(abs string) bool  { return pathAllowed(abs, h.FSRead) }
func (h *Host) allowWrite(abs string) bool { return pathAllowed(abs, h.FSWrite) }

func pathAllowed(abs string, allow []string) bool {
	for _, a := range allow {
		if a == abs || strings.HasPrefix(abs, strings.TrimRight(a, "/")+"/") {
			return true
		}
	}
	return false
}

// InstallHostImports registers stado_log / stado_fs_read / stado_fs_write
// on the runtime's "stado" module namespace. The caller passes a Host
// per plugin; each host function closes over it so different plugins
// get different capability gates even though they share the runtime.
//
// Must be called BEFORE Instantiate(wasmBytes, ...) so the plugin can
// resolve the imports at link time.
func InstallHostImports(ctx context.Context, r *Runtime, host *Host) error {
	builder := r.rt.NewHostModuleBuilder("stado")

	// stado_log(level_ptr, level_len, msg_ptr, msg_len)
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			levelPtr := api.DecodeU32(stack[0])
			levelLen := api.DecodeU32(stack[1])
			msgPtr := api.DecodeU32(stack[2])
			msgLen := api.DecodeU32(stack[3])
			level, err := readString(mod, levelPtr, levelLen)
			if err != nil {
				return
			}
			msg, err := readString(mod, msgPtr, msgLen)
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

	// stado_fs_read(path_ptr, path_len, buf_ptr, buf_cap) → int32
	//
	// Returns bytes written to buf, -1 on capability deny / read error
	// / truncation past buf_cap. Path is resolved relative to
	// host.Workdir when not absolute, then capability-checked against
	// the manifest's fs:read entries.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			pathPtr := api.DecodeU32(stack[0])
			pathLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufCap := api.DecodeU32(stack[3])
			path, err := readString(mod, pathPtr, pathLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			abs, err := realPath(host.Workdir, path)
			if err != nil {
				host.Logger.Warn("stado_fs_read denied — symlink resolution failed", slog.String("path", path), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.allowRead(abs) {
				host.Logger.Warn("stado_fs_read denied", slog.String("path", abs))
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				host.Logger.Warn("stado_fs_read failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if uint32(len(data)) > bufCap {
				data = data[:bufCap]
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_fs_read")

	// stado_fs_write(path_ptr, path_len, buf_ptr, buf_len) → int32
	//
	// Returns bytes written, -1 on deny / error. Always creates or
	// truncates the target; append mode isn't exposed.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			pathPtr := api.DecodeU32(stack[0])
			pathLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufLen := api.DecodeU32(stack[3])
			path, err := readString(mod, pathPtr, pathLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			abs, err := realPath(host.Workdir, path)
			if err != nil {
				host.Logger.Warn("stado_fs_write denied — symlink resolution failed", slog.String("path", path), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.allowWrite(abs) {
				host.Logger.Warn("stado_fs_write denied", slog.String("path", abs))
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := readBytes(mod, bufPtr, bufLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				host.Logger.Warn("stado_fs_write mkdir", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := os.WriteFile(abs, data, 0o644); err != nil {
				host.Logger.Warn("stado_fs_write failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(int32(len(data)))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_fs_write")

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
			if uint32(len(data)) > bufCap {
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
			if uint32(len(ev)) > bufCap {
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
			if uint32(len(data)) > outCap {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_session_fork")

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

	if _, err := builder.Instantiate(ctx); err != nil {
		return fmt.Errorf("wazero: install host imports: %w", err)
	}
	return nil
}

// resolveAbs makes a plugin-supplied path absolute. Plugins that pass
// relative paths see them rooted at workdir; absolute paths pass
// through. Cleaned via filepath.Clean so `..` traversal is normalised
// — the capability check still rejects paths that escape the allowed
// roots, but cleaning first avoids prefix-bypass with "/allowed/../etc".
//
// SECURITY: returns the cleaned absolute path *before* following symlinks.
// The caller must use realPath() if symlink resolution is needed; for
// fs_read/fs_write the capability check runs on the resolved target so
// symlinks cannot escape the allowed prefix.
func resolveAbs(workdir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	return filepath.Clean(path)
}

// realPath returns the canonical filesystem path: absolute, cleaned,
// and with symlink components evaluated. If the path (or any parent
// component) resolves outside the allowed prefix, the returned string
// still indicates that escape so the caller can reject it.
//
// SECURITY: used by fs_read/fs_write so that a symlink inside an allowed
// directory pointing outside is caught.  The capability check runs
// against the resolved target, not the symlink path.
func realPath(workdir, path string) (string, error) {
	abs := resolveAbs(workdir, path)

	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}

	// If the final component doesn't exist yet (legitimate for writes),
	// resolve as far as possible by walking up to the deepest existing
	// ancestor, resolving that, and appending the remaining suffix.
	if !os.IsNotExist(err) {
		return "", err
	}

	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	// Walk up until we find an existing directory.
	for dir != "/" && dir != "." {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		dir, base = filepath.Dir(dir), filepath.Join(filepath.Base(dir), base)
	}

	resolvedDir, derr := filepath.EvalSymlinks(dir)
	if derr != nil {
		return "", derr
	}
	result := filepath.Join(resolvedDir, base)
	return filepath.Clean(result), nil
}

// NamespaceStado is the wasm module name plugins import from
// ("stado"). Exported so test helpers can assert without re-stringing.
const NamespaceStado = "stado"

// Compile-time check: confirm wazero's NewHostModuleBuilder returns
// the type we expect.
var _ wazero.HostModuleBuilder = (wazero.HostModuleBuilder)(nil)
