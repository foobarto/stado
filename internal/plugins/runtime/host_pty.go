package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// expectMaxPatterns caps how many patterns one Expect call may scan
// for. 16 is a generous ceiling — multi-pattern expect is mostly used
// for "either a prompt or an error" two- or three-way branching, not
// long lists. The cap also bounds regex compilation cost.
const expectMaxPatterns = 16

const (
	maxPluginRuntimePTYInputBytes  uint32 = 64 << 10
	maxPluginRuntimePTYWriteBytes  uint32 = 1 << 20
	maxPluginRuntimePTYReadCap     uint32 = 4 << 20
	pluginRuntimePTYDefaultTimeout        = 100 * time.Millisecond
)

// registerPTYImports wires the PTY plugin host imports: create / list /
// write / read / signal / resize / destroy / snapshot / expect (EP-0043
// removed attach/detach — access is by session id). All are gated on the
// "exec:pty" capability — if
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
	// EP-0038b: always register PTY imports so wasm modules that link
	// against them (e.g. the bundled shell plugin) can instantiate even
	// when the plugin lacks exec:pty. The handlers check the capability
	// at call time and return an error if not granted.
	registerPTYCreate(builder, host, "stado_pty_create")
	registerPTYList(builder, host, "stado_pty_list")
	// EP-0043 D6: stado_*_attach / stado_*_detach removed — read/write
	// work by id, no attach gate.
	registerPTYWrite(builder, host, "stado_pty_write")
	registerPTYRead(builder, host, "stado_pty_read")
	registerPTYSignal(builder, host, "stado_pty_signal")
	registerPTYResize(builder, host, "stado_pty_resize")
	registerPTYDestroy(builder, host, "stado_pty_destroy")
	registerPTYSnapshot(builder, host, "stado_pty_snapshot")
	registerPTYExpect(builder, host, "stado_pty_expect")
}

// ptyDenied returns the i64-encoded error response for PTY handlers when
// the capability is not granted or no PTY manager is wired.
func ptyDenied(mod api.Module, resPtr, resCap uint32) int32 {
	return encodeToolSidePayload(mod, resPtr, resCap, []byte("exec:pty capability required"))
}

// stado_pty_create(args_ptr, args_len, result_ptr, result_cap) → i64
//
// args = JSON pty.SpawnOpts. Returns the new id on success (always
// >0). On error, writes error string to result and returns -length.
func registerPTYCreate(builder wazero.HostModuleBuilder, host *Host, exportName string) {
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
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI64(int64(ptyDenied(mod, resPtr, resCap)))
				return
			}
			// Enforce the exec:pty glob on the spawned binary —
			// parity with the exec:proc path. Without this, exec:pty alone (or a
			// narrow exec:pty:<glob>) could run ANY binary, and opts.Cmd expands
			// to "/bin/sh -c <cmd>" (an unrestricted shell) inside Manager.Spawn.
			bin := "/bin/sh" // the Manager.Spawn fallback when Argv is empty
			if len(opts.Argv) > 0 {
				bin = opts.Argv[0]
			}
			if !host.ptyAllowed(bin) {
				host.Logger.Warn("stado_pty_create denied by cap", slog.String("bin", bin))
				stack[0] = api.EncodeI64(int64(ptyDenied(mod, resPtr, resCap)))
				return
			}
			// Confine the PTY child whenever the surface sets a default sandbox
			// policy, at parity with stado_exec (#100). The cap check above
			// already ran on the original binary; the wrap rewrites argv to the
			// surface's sandbox runner.
			sopts, serr := sandboxPTYSpawnOpts(host, opts)
			if serr != nil {
				host.Logger.Warn("stado_pty_create sandbox wrap failed", slog.String("err", serr.Error()))
				stack[0] = api.EncodeI64(int64(encodeToolSidePayload(mod, resPtr, resCap, []byte(serr.Error()))))
				return
			}
			id, err := host.PTYManager.Spawn(sopts)
			if err != nil {
				host.Logger.Warn("stado_pty_create failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI64(int64(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error()))))
				return
			}
			stack[0] = api.EncodeI64(int64(id))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64}).
		Export(exportName)
}

// sandboxPTYSpawnOpts wraps the PTY spawn command in the host's default sandbox
// runner when one is set (#100). Mirrors stado_exec's buildSandboxedCmd path so
// a PTY shell is confined by the same runner as a one-shot exec. When no host
// default is set, opts is returned unchanged. resolveSandboxPolicy(host, nil)
// yields the host default or nil, so the gate matches the exec path exactly.
//
// It sets opts.PreparedCmd to the runner-wrapped *exec.Cmd (e.g. bwrap … --
// argv); the PTY manager starts THAT under a pty, so the real shell runs inside
// the sandbox namespace. Argv/Cmd are left as the original for List display.
func sandboxPTYSpawnOpts(host *Host, opts pty.SpawnOpts) (pty.SpawnOpts, error) {
	policy := resolveSandboxPolicy(host, nil)
	if policy == nil {
		return opts, nil
	}
	eff := opts.Argv
	if len(eff) == 0 {
		if opts.Cmd == "" {
			return opts, nil // Manager.Spawn returns ErrNoCommand
		}
		eff = []string{"/bin/sh", "-c", opts.Cmd}
	}
	// Preserve the caller's cwd (Codex #213 P2): only fall back to the host
	// workdir when the spawn didn't request one, matching the unsandboxed path
	// (Manager.Spawn honors opts.Cwd).
	workdir := opts.Cwd
	if workdir == "" {
		workdir = host.Workdir
	}
	// Use a background context, NOT the host-import call's ctx (Codex #213 P2):
	// a PTY session is persistent and outlives the stado_pty_create call, so
	// tying the sandboxed child to a ctx that's canceled when the tool returns
	// (or hits a per-call timeout) would kill the session. The Manager owns the
	// lifecycle (Destroy kills the process), same as the unsandboxed
	// exec.Command path.
	cmd, err := buildSandboxedCmdWithRunner(context.Background(), sandboxRunnerForHost(host), policy, workdir, eff, opts.Env)
	if err != nil {
		return opts, err
	}
	// Hand the runner's *exec.Cmd to the manager intact — it carries ExtraFiles
	// (the seccomp BPF fd that `--seccomp <fd>` references) and SysProcAttr that
	// a re-derived exec.Command from argv would drop, producing
	// "bwrap: Can't read seccomp data: Bad file descriptor". Argv/Cmd are left
	// as the original so List shows the real command, not the bwrap wrapper.
	opts.PreparedCmd = cmd
	return opts, nil
}

// stado_pty_list(buf_ptr, buf_cap) → i32
//
// Writes JSON array of pty.SessionInfo. Returns byte count or -length
// on error.
func registerPTYList(builder wazero.HostModuleBuilder, host *Host, exportName string) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			bufPtr := api.DecodeU32(stack[0])
			bufCap := api.DecodeU32(stack[1])
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, bufPtr, bufCap))
				return
			}
			infos := host.PTYManager.List()
			payload, err := json.Marshal(infos)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, bufPtr, bufCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, payload))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
}

// stado_pty_write(id_lo, id_hi, buf_ptr, buf_len, err_ptr, err_cap) → i32
//
// Returns bytes written, -length with err string on failure. The id
// is split across two i32s because wasm32 host imports cap params at
// i32 unless explicitly declared i64; for Write we want the buffer
// pointer/length pair to stay i32-aligned.
func registerPTYWrite(builder wazero.HostModuleBuilder, host *Host, exportName string) {
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
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, errPtr, errCap))
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
		Export(exportName)
}

// stado_pty_read(id_lo, id_hi, max_bytes, timeout_ms, buf_ptr, buf_cap) → i32
//
// Returns bytes read (positive, may be 0 on timeout-with-no-data), -1
// when the session has closed and the ring is empty (EOF), or
// -length with err string on other failure.
func registerPTYRead(builder wazero.HostModuleBuilder, host *Host, exportName string) {
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
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, bufPtr, bufCap))
				return
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
		Export(exportName)
}

// stado_pty_signal(args_ptr, args_len, result_ptr, result_cap) → i32
//
// args = {"id": uint64, "sig": int|string}. sig is a POSIX signal number
// (e.g. 2 = SIGINT, 15 = SIGTERM) OR a name ("SIGINT", "TERM", case-
// insensitive, with or without the SIG prefix) — the shell__signal tool
// description advertises names, so the host resolves them. Returns 0 on
// success.
func registerPTYSignal(builder wazero.HostModuleBuilder, host *Host, exportName string) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID  uint64          `json:"id"`
				Sig json.RawMessage `json:"sig"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			sig, err := parseSignal(req.Sig)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, resPtr, resCap))
				return
			}
			if err := host.PTYManager.Signal(req.ID, sig); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
}

// signalNames maps POSIX signal names (sans SIG prefix, upper-case) to
// numbers. Covers the signals an agent realistically sends to a PTY child;
// numeric sig works for anything outside the table. Less common Linux signals
// (USR1/USR2/STOP/CONT/TSTP/WINCH) are registered by an init in
// host_pty_signals_unix.go.
var signalNames = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
	"KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM,
}

// parseSignal accepts a JSON number (15) or a name string ("SIGTERM" /
// "term"). The shell__signal tool documents both forms, so a string that
// the host silently failed to decode-as-int was a real footgun.
func parseSignal(raw json.RawMessage) (syscall.Signal, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, fmt.Errorf("sig is required")
	}
	if raw[0] == '"' {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return 0, fmt.Errorf("sig: %w", err)
		}
		key := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "SIG")
		if sig, ok := signalNames[key]; ok {
			return sig, nil
		}
		if n, err := strconv.Atoi(key); err == nil { // numeric string e.g. "9"
			return syscall.Signal(n), nil
		}
		return 0, fmt.Errorf("sig: unknown signal name %q", name)
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("sig: %w", err)
	}
	return syscall.Signal(n), nil
}

// stado_pty_resize(args_ptr, args_len, result_ptr, result_cap) → i32
func registerPTYResize(builder wazero.HostModuleBuilder, host *Host, exportName string) {
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
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, resPtr, resCap))
				return
			}
			if err := host.PTYManager.Resize(req.ID, req.Cols, req.Rows); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
}

// stado_pty_destroy(args_ptr, args_len, result_ptr, result_cap) → i32
func registerPTYDestroy(builder wazero.HostModuleBuilder, host *Host, exportName string) {
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
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, resPtr, resCap))
				return
			}
			if err := host.PTYManager.Destroy(req.ID); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
}

// stado_pty_snapshot(args_ptr, args_len, result_ptr, result_cap) → i32
//
// args = {"id": uint64, "with_svg"?: bool, "svg_cell_w"?: float,
//
//	"svg_cell_h"?: float, "svg_font_px"?: int}
//
// Returns byte count of JSON written to result on success, -length
// with err string on failure. Result JSON shape:
//
//	{"text": "...", "cols": 120, "rows": 32,
//	 "cursor": {"x": 12, "y": 5, "visible": true},
//	 "title": "...", "svg"?: "<svg>...</svg>"}
//
// The text field is always present (lossy plain rendering — see
// pty.Screen.Text). svg is only emitted when with_svg=true; for a
// 120×32 grid SVG runs ~30–60 KB so this is opt-in to keep the
// default snapshot cheap.
func registerPTYSnapshot(builder wazero.HostModuleBuilder, host *Host, exportName string) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])
			var req struct {
				ID        uint64  `json:"id"`
				Mode      string  `json:"mode"` // EP-0043: "auto" | "screen" (default "screen")
				WithSVG   bool    `json:"with_svg"`
				SVGCellW  float64 `json:"svg_cell_w"`
				SVGCellH  float64 `json:"svg_cell_h"`
				SVGFontPx int     `json:"svg_font_px"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, resPtr, resCap))
				return
			}
			// EP-0043 mode:auto — when no full-screen program is active, the
			// raw stream is the right view, so return a {"kind":"stream"}
			// marker (the read tool then pulls the stream) WITHOUT paying for
			// a grid render. AltScreen is a cheap bitfield read.
			if req.Mode == "auto" {
				alt, err := host.PTYManager.AltScreen(req.ID)
				if err != nil {
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
					return
				}
				if !alt {
					payload, _ := json.Marshal(map[string]any{"kind": "stream"})
					stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
					return
				}
				// alt-screen active: we're about to return a rendered grid.
				// Discard the raw ring so the full-screen program's escape
				// backlog doesn't resurface in a later mode:stream/auto read
				// once it leaves the alternate buffer. The grid (vt10x) is a
				// separate sink, so the render below is unaffected. Only the
				// auto path drains — explicit mode:"screen" stays a
				// non-draining peek (e.g. the TUI overlay polls read-only).
				_, _ = host.PTYManager.DiscardPending(req.ID)
			}
			screen, err := host.PTYManager.Snapshot(req.ID)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			out := map[string]any{
				"kind":  "screen",
				"text":  screen.Text(),
				"cols":  screen.Cols,
				"rows":  screen.Rows,
				"title": screen.Title,
				"cursor": map[string]any{
					"x":       screen.Cursor.X,
					"y":       screen.Cursor.Y,
					"visible": screen.Cursor.Visible,
				},
			}
			if req.WithSVG {
				opts := &pty.SVGOpts{
					CellW:  req.SVGCellW,
					CellH:  req.SVGCellH,
					FontPx: req.SVGFontPx,
				}
				out["svg"] = screen.SVG(opts)
			}
			payload, err := json.Marshal(out)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
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

// stado_pty_expect(idLo, idHi, argsPtr, argsLen, resPtr, resCap) → i32
//
// Reads from an existing PTY session until a configured pattern matches,
// the timeout elapses, or the underlying process exits. Args JSON shape:
//
//	{"patterns": ["password:", "$ "], "regex"?: false, "timeout_ms"?: 30000}
//
// Returns byte count of the result JSON written at resPtr, or
// -byte_count with an error string at resPtr on validation / dispatch
// failure. Result JSON discriminates on matched/timeout/eof:
//
//	match  : {"matched": true, "pattern_index": N, "before": <b64>, "match": <b64>}
//	timeout: {"matched": false, "timeout": true, "before": <b64>}
//	eof    : {"matched": false, "eof": true, "before": <b64>, "exit_code": N}
//
// Pattern bytes are base64-encoded because PTY output routinely
// includes non-UTF8 sequences (ANSI escapes, terminal control codes).
// JSON-string encoding would corrupt them.
//
// Cap-gated by exec:pty — same gate as Read/Write.
func registerPTYExpect(builder wazero.HostModuleBuilder, host *Host, exportName string) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			idLo := api.DecodeU32(stack[0])
			idHi := api.DecodeU32(stack[1])
			argsPtr := api.DecodeU32(stack[2])
			argsLen := api.DecodeU32(stack[3])
			resPtr := api.DecodeU32(stack[4])
			resCap := api.DecodeU32(stack[5])
			id := uint64(idHi)<<32 | uint64(idLo)

			var req struct {
				Patterns  []string `json:"patterns"`
				Regex     bool     `json:"regex"`
				TimeoutMs int      `json:"timeout_ms"`
			}
			if err := decodePTYArgs(mod, argsPtr, argsLen, &req); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if len(req.Patterns) == 0 {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte("expect: patterns required")))
				return
			}
			if len(req.Patterns) > expectMaxPatterns {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte("expect: too many patterns")))
				return
			}
			if req.TimeoutMs < 0 {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte("expect: timeout_ms must be >= 0")))
				return
			}

			patterns := make([]pty.Pattern, 0, len(req.Patterns))
			for i, raw := range req.Patterns {
				if raw == "" {
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte("expect: empty pattern")))
					return
				}
				if req.Regex {
					re, err := regexp.Compile(raw)
					if err != nil {
						msg := "expect: pattern[" + strconv.Itoa(i) + "]: invalid regex: " + err.Error()
						stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(msg)))
						return
					}
					patterns = append(patterns, pty.Pattern{Regex: re})
				} else {
					patterns = append(patterns, pty.Pattern{Bytes: []byte(raw)})
				}
			}

			if !host.ExecPTY || host.PTYManager == nil {
				stack[0] = api.EncodeI32(ptyDenied(mod, resPtr, resCap))
				return
			}

			timeout := time.Duration(req.TimeoutMs) * time.Millisecond
			res, err := host.PTYManager.Expect(id, patterns, timeout)
			if err != nil {
				host.Logger.Warn("stado_pty_expect failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}

			out := map[string]any{
				"matched": res.Matched,
				"before":  base64.StdEncoding.EncodeToString(res.Before),
			}
			switch {
			case res.Matched:
				out["pattern_index"] = res.PatternIndex
				out["match"] = base64.StdEncoding.EncodeToString(res.Match)
			case res.Timeout:
				out["timeout"] = true
			case res.EOF:
				out["eof"] = true
				out["exit_code"] = res.ExitCode
			}
			payload, err := json.Marshal(out)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
}
