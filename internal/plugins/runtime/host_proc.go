package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// procHandle holds the state for a long-lived spawned process.
type procHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// procAllowed checks exec:proc / exec:proc:<glob> capability.
func (host *Host) procAllowed(bin string) bool {
	if !host.ExecProc {
		return false
	}
	if host.ExecProcGlob == "" {
		return true // broad exec:proc
	}
	// Scoped: resolve binary and match glob.
	abs, err := exec.LookPath(bin)
	if err != nil {
		abs = bin
	}
	abs = filepath.Clean(abs)
	matched, _ := filepath.Match(host.ExecProcGlob, abs)
	return matched
}

func registerProcImports(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	registerExecImport(builder, host)
	registerProcSpawnImport(builder, host, rt)
	registerProcReadImport(builder, host, rt)
	registerProcWriteImport(builder, host, rt)
	registerProcWaitImport(builder, host, rt)
	registerProcKillImport(builder, host, rt)
	registerProcCloseImport(builder, host, rt)
}

// registerExecImport registers stado_exec — one-shot process run.
// stado_exec(req_ptr, req_len, result_ptr, result_cap) → int32
// req/result are JSON-encoded ExecRequest / ExecResult.
func registerExecImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])

			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 64<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				Argv    []string `json:"argv"`
				Stdin   string   `json:"stdin"`
				Env     []string `json:"env"`
				Timeout int      `json:"timeout_ms"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || len(req.Argv) == 0 {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.procAllowed(req.Argv[0]) {
				host.Logger.Warn("stado_exec denied by cap", slog.String("bin", req.Argv[0]))
				type errResult struct {
					Error string `json:"error"`
				}
				b, _ := json.Marshal(errResult{Error: fmt.Sprintf("exec:proc cap required for %q", req.Argv[0])})
				stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, b))
				return
			}
			timeout := 30 * time.Second
			if req.Timeout > 0 {
				timeout = time.Duration(req.Timeout) * time.Millisecond
			}
			execCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(execCtx, req.Argv[0], req.Argv[1:]...) //nolint:gosec
			cmd.Dir = host.Workdir
			if len(req.Env) > 0 {
				cmd.Env = req.Env
			}
			if req.Stdin != "" {
				cmd.Stdin = strings.NewReader(req.Stdin)
			}
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out

			exitCode := 0
			runErr := ""
			if err := cmd.Run(); err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				} else {
					runErr = err.Error()
				}
			}
			type result struct {
				Stdout   string `json:"stdout"`
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error,omitempty"`
			}
			payload, _ := json.Marshal(result{
				Stdout:   out.String(),
				ExitCode: exitCode,
				Error:    runErr,
			})
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_exec")
}

// registerProcSpawnImport registers stado_proc_spawn.
// stado_proc_spawn(req_ptr, req_len) → handle (u32), 0 on error
func registerProcSpawnImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 64<<10)
			if err != nil {
				stack[0] = 0
				return
			}
			var req struct {
				Argv []string `json:"argv"`
				Env  []string `json:"env"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil || len(req.Argv) == 0 {
				stack[0] = 0
				return
			}
			if !host.procAllowed(req.Argv[0]) {
				host.Logger.Warn("stado_proc_spawn denied by cap", slog.String("bin", req.Argv[0]))
				stack[0] = 0
				return
			}
			cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...) //nolint:gosec
			cmd.Dir = host.Workdir
			if len(req.Env) > 0 {
				cmd.Env = req.Env
			}
			stdinPipe, err := cmd.StdinPipe()
			if err != nil {
				stack[0] = 0
				return
			}
			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				_ = stdinPipe.Close()
				stack[0] = 0
				return
			}
			if err := cmd.Start(); err != nil {
				host.Logger.Warn("stado_proc_spawn failed", slog.String("err", err.Error()))
				stack[0] = 0
				return
			}
			h := rt.handles.alloc("proc", &procHandle{cmd: cmd, stdin: stdinPipe, stdout: stdoutPipe})
			stack[0] = uint64(h)
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_proc_spawn")
}

// stado_proc_read(h, max, timeout_ms, buf_ptr, buf_cap) → int32
func registerProcReadImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			h := uint32(stack[0])
			max := api.DecodeU32(stack[1])
			// timeout_ms at stack[2] — TODO: apply read deadline
			bufPtr := api.DecodeU32(stack[3])
			bufCap := api.DecodeU32(stack[4])
			v, ok := rt.handles.get(h)
			if !ok || !rt.handles.isType(h, "proc") {
				stack[0] = api.EncodeI32(-1)
				return
			}
			ph := v.(*procHandle) //nolint:forcetypeassert
			if max > bufCap {
				max = bufCap
			}
			buf := make([]byte, max)
			n, err := ph.stdout.Read(buf)
			if err != nil || n == 0 {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, buf[:n]))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_proc_read")
}

// stado_proc_write(h, buf_ptr, buf_len) → int32
func registerProcWriteImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			h := uint32(stack[0])
			bufPtr := api.DecodeU32(stack[1])
			bufLen := api.DecodeU32(stack[2])
			v, ok := rt.handles.get(h)
			if !ok || !rt.handles.isType(h, "proc") {
				stack[0] = api.EncodeI32(-1)
				return
			}
			ph := v.(*procHandle) //nolint:forcetypeassert
			data, err := readBytesLimited(mod, bufPtr, bufLen, uint32(maxPluginRuntimeFSFileBytes))
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			n, err := ph.stdin.Write(data)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			encoded, ok2 := encodeI32Length(n)
			if !ok2 {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = encoded
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_proc_write")
}

// stado_proc_wait(h) → exit_code (i32), -1 on error
func registerProcWaitImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			h := uint32(stack[0])
			v, ok := rt.handles.get(h)
			if !ok || !rt.handles.isType(h, "proc") {
				stack[0] = api.EncodeI32(-1)
				return
			}
			ph := v.(*procHandle) //nolint:forcetypeassert
			if err := ph.cmd.Wait(); err != nil {
				if ee, ok2 := err.(*exec.ExitError); ok2 {
					stack[0] = api.EncodeI32(int32(ee.ExitCode())) //nolint:gosec
					return
				}
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(0)
		}),
		[]api.ValueType{api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_proc_wait")
}

// stado_proc_kill(h, signal) — no return value
func registerProcKillImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			h := uint32(stack[0])
			if v, ok := rt.handles.get(h); ok && rt.handles.isType(h, "proc") {
				ph := v.(*procHandle) //nolint:forcetypeassert
				if ph.cmd.Process != nil {
					_ = ph.cmd.Process.Kill()
				}
			}
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{}).
		Export("stado_proc_kill")
}

// stado_proc_close(h) — kill + free handle
func registerProcCloseImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			h := uint32(stack[0])
			if v, ok := rt.handles.get(h); ok && rt.handles.isType(h, "proc") {
				ph := v.(*procHandle) //nolint:forcetypeassert
				_ = ph.stdin.Close()
				if ph.cmd.Process != nil {
					_ = ph.cmd.Process.Kill()
				}
			}
			rt.handles.free(h)
		}),
		[]api.ValueType{api.ValueTypeI32},
		[]api.ValueType{}).
		Export("stado_proc_close")
}
