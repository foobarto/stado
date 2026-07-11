package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/pkg/tool"
)

// procHandle holds the state for a long-lived spawned process.
type procHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// cappedOutput retains at most limit bytes while reporting full writes to the
// child process, so stdout/stderr continue to drain without unbounded memory.
type cappedOutput struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

const maxProcCaptureBytes = 1 << 20

func procCaptureLimit(claimed uint32) int {
	if claimed > maxProcCaptureBytes {
		return maxProcCaptureBytes
	}
	return int(claimed)
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	written := len(p)
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = w.buf.Write(p[:remaining])
	}
	if remaining < len(p) {
		w.overflow = true
	}
	return written, nil
}

func (w *cappedOutput) String() string { return w.buf.String() }

// procAllowed checks exec:proc / exec:proc:<glob> capability.
//
// Glob forms (EP-no-internal-tools Step 3):
//   - Absolute path: matched against the resolved absolute path
//     (`exec:proc:/usr/bin/bash`, `exec:proc:/usr/bin/impacket-*`)
//   - Slash-free basename: accepted only for a bare-name argv[0], then matched
//     against `filepath.Base(resolved)` (`exec:proc:bash` matches argv[0]
//     `bash`, whose resolution is left to PATH)
//   - Mixed forms (relative path with slashes, e.g. `bin/bash`) are
//     rejected as ambiguous.
func (h *Host) procAllowed(bin string) bool {
	return h.ExecProc && execGlobMatch(bin, h.ExecProcGlobs)
}

// ptyAllowed checks the exec:pty / terminal:open capability + its glob set for a
// PTY-spawned binary — the analogue of procAllowed. Without this the PTY path
// ran any binary regardless of the (narrower) exec:proc glob, bypassing
// cap-confinement.
func (h *Host) ptyAllowed(bin string) bool {
	return h.ExecPTY && execGlobMatch(bin, h.ExecPTYGlobs)
}

// execGlobMatch reports whether bin matches any of globs. Empty globs = broad
// (match-all). Glob forms: absolute path (matched against the resolved abs
// path) or slash-free basename (matched against filepath.Base). Shared by
// procAllowed and ptyAllowed so the two exec surfaces can't drift.
func execGlobMatch(bin string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	abs, err := exec.LookPath(bin)
	if err != nil {
		abs = bin
	}
	abs = filepath.Clean(abs)
	base := filepath.Base(abs)
	// A path-containing argv[0] (absolute or relative) must be authorized by an
	// absolute-path cap, NEVER by a basename cap (Codex #44). Otherwise
	// `exec:proc:git` would authorize /tmp/evil/git or ./git just because the
	// basename matches, while a different binary than the operator scoped to
	// gets executed. A basename cap only authorizes a bare-name argv[0], whose
	// resolution is left to PATH. Detect a path in a filepath-aware way: any
	// `/` (universal), any `\` (Windows separator; on Unix it's a rare-but-
	// legal filename char, so treating it as a path fails closed — safe), or a
	// volume prefix like `C:` (Windows drive-relative, no separator).
	binHasPath := strings.ContainsAny(bin, `/\`) || filepath.VolumeName(bin) != ""
	for _, glob := range globs {
		if strings.Contains(glob, "/") {
			// Absolute-path form (relative glob with slashes was rejected at
			// cap-parse time).
			if matched, _ := filepath.Match(glob, abs); matched {
				return true
			}
		} else if !binHasPath {
			// Slash-free basename glob — only for a bare-name argv[0].
			if matched, _ := filepath.Match(glob, base); matched {
				return true
			}
		}
	}
	return false
}

// execGlobDeny is appended in place of a REJECTED (malformed) glob so the cap
// stays SCOPED (a non-empty glob set ⇒ not broad) yet matches nothing — a
// malformed scoped exec cap fails CLOSED (deny-all) rather than silently
// degrading to broad access (which would reopen the confinement bypass for
// exactly the invalid-glob case the parser rejects). The NUL byte can't appear
// in a real binary path or basename, so execGlobMatch never matches it.
const execGlobDeny = "\x00deny"

// appendExecGlob validates an exec:proc / exec:pty / terminal:open glob and
// appends it. A mixed relative-path form (contains a slash but isn't absolute)
// is rejected — it can't match execGlobMatch's resolved-path/basename lookup
// and would be a silent-deny footgun. On rejection it appends execGlobDeny so
// the scoped cap fails closed (deny-all) instead of degrading to broad.
func (h *Host) appendExecGlob(dst []string, glob, capName string) []string {
	if strings.Contains(glob, "/") && !strings.HasPrefix(glob, "/") {
		if h.Logger != nil {
			h.Logger.Warn(capName+" glob rejected: mixed relative-path form (use absolute path or slash-free basename); capability now denies all",
				slog.String("glob", glob))
		}
		return append(dst, execGlobDeny)
	}
	return append(dst, glob)
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
//
// EP-no-internal-tools Step 3: req gains an optional `sandbox` field
// — when set, the call routes through sandbox.Runner with that policy.
// When nil, runs unsandboxed (today's behavior). Plugin author decides;
// stado is unbiased.
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
				Argv    []string       `json:"argv"`
				Stdin   string         `json:"stdin"`
				Env     []string       `json:"env"`
				Timeout int            `json:"timeout_ms"`
				Sandbox *sandboxPolicy `json:"sandbox,omitempty"`
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

			cmd, cmdErr := buildSandboxedCmdWithRunner(execCtx, sandboxRunnerForHost(host), resolveSandboxPolicy(host, req.Sandbox), host.Workdir, req.Argv, req.Env)
			if cmdErr != nil {
				type errResult struct {
					Error string `json:"error"`
				}
				b, _ := json.Marshal(errResult{Error: cmdErr.Error()})
				stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, b))
				return
			}
			if req.Stdin != "" {
				cmd.Stdin = strings.NewReader(req.Stdin)
			}
			out := cappedOutput{limit: procCaptureLimit(resCap)}
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
			if out.overflow || byteLenExceedsCap(payload, resCap) {
				msg := fmt.Sprintf("exec: response exceeds %d-byte result limit", resCap)
				if exitCode != 0 {
					msg = fmt.Sprintf("command exited with code %d\n%s", exitCode, msg)
					failureCode := exitCode
					structured, _ := json.Marshal(tool.ErrorEnvelopeV1{
						Schema: tool.ErrorEnvelopeSchemaV1, Kind: tool.FailureExit,
						Message: msg, ExitCode: &failureCode,
					})
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, structured))
					return
				} else if runErr != "" {
					msg = runErr + "\n" + msg
				} else {
					// The command succeeded; bound its output instead of converting
					// success into a launch failure. Verification can then continue
					// to later gates rather than fail-open on infrastructure posture.
					bounded, _ := json.Marshal(result{Stdout: "[output omitted: " + msg + "]"})
					stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, bounded))
					return
				}
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(msg)))
				return
			}
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
				Argv    []string       `json:"argv"`
				Env     []string       `json:"env"`
				Sandbox *sandboxPolicy `json:"sandbox,omitempty"`
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
			cmd, cmdErr := buildSandboxedCmdWithRunner(ctx, sandboxRunnerForHost(host), resolveSandboxPolicy(host, req.Sandbox), host.Workdir, req.Argv, req.Env)
			if cmdErr != nil {
				host.Logger.Warn("stado_proc_spawn sandbox build failed", slog.String("err", cmdErr.Error()))
				stack[0] = 0
				return
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
			h, err := rt.handles.alloc("proc", &procHandle{cmd: cmd, stdin: stdinPipe, stdout: stdoutPipe})
			if err != nil {
				host.Logger.Warn("stado_proc_spawn failed", slog.String("err", err.Error()))
				_ = stdinPipe.Close()
				_ = stdoutPipe.Close()
				_ = cmd.Process.Kill()
				stack[0] = 0
				return
			}
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
			timeoutMS := api.DecodeU32(stack[2])
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
			// Honor the plugin-supplied timeout: a positive timeout_ms bounds the
			// read so a plugin can poll a quiet subprocess without blocking the
			// wasm call indefinitely. The stdout pipe is an *os.File, which
			// supports SetReadDeadline; a reader without deadline support ignores
			// it. A timed-out (or any) read returns -1, matching the existing ABI.
			n, err := readProcWithDeadline(ph.stdout, buf, timeoutMS)
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

// readProcWithDeadline reads once from r into buf, applying a read deadline of
// timeoutMS milliseconds when timeoutMS > 0 and r supports deadlines (the
// subprocess stdout pipe is an *os.File, which does). timeoutMS == 0 means
// block (the prior behavior). The deadline is cleared afterward so a later
// blocking read on the same handle isn't affected. Returns the bytes read and
// any error (incl. a deadline-exceeded timeout).
func readProcWithDeadline(r io.Reader, buf []byte, timeoutMS uint32) (int, error) {
	if timeoutMS > 0 {
		if d, ok := r.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = d.SetReadDeadline(time.Now().Add(time.Duration(timeoutMS) * time.Millisecond))
			defer func() { _ = d.SetReadDeadline(time.Time{}) }()
		}
	}
	return r.Read(buf)
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

// NewDefaultSandboxPolicy returns the host-default sandbox policy for
// entry points that auto-confine stado_exec / stado_proc_spawn calls.
// The policy is conservatively permissive — runs
// the child under bwrap / sandbox-exec for PID + uid namespace
// isolation, allows reading the system paths bash typically needs
// (/bin, /sbin, /tmp, /run; /usr, /lib, /lib64, /etc, /proc, /dev are
// bound automatically by the runner), and lets network through.
//
// Earlier versions of this function returned `&sandboxPolicy{CWD:
// workdir}` and claimed that produced unrestricted FS/net. That was
// wrong on two counts (caught in 2026-05-09 second-pass review):
//
//   - sandboxPolicy.Net is a string; the empty default falls through
//     both translation cases in buildSandboxedCmd's switch, leaving
//     sandbox.NetPolicy.Kind at its zero value of NetDenyAll → bwrap
//     gets --unshare-net. So "no net restrictions" actually meant
//     "no network at all".
//
//   - The runner mounts /usr, /lib, /lib64, /etc but NOT /bin or
//     /sbin. The shell wasm calls /bin/sh and /bin/bash literals; on
//     distros where /bin isn't symlinked through /usr (Debian, some
//     containers), `bwrap … /bin/sh` fails with "execvp: No such file
//     or directory" before the command runs.
//
// The values below fix both. /bin and /sbin are bound (--ro-bind-try
// is a no-op when they don't exist or are already covered by /usr's
// symlink resolution). /tmp + /var/tmp are writable so plugins that
// scratch there work. Net is explicit "allow".
//
// Operators wanting tighter rules supply an explicit `sandbox` field
// on each stado_exec request from the wasm side. The intersection
// resolver (resolveSandboxPolicy below) ensures the guest can only
// TIGHTEN this default — never weaken it. Plugin authors cannot opt
// out of host policy via the wasm side; if a host default is set,
// `unsandboxed: true` is ignored.
//
// Returns *sandboxPolicy as any so cmd/stado can stash it on a
// tool.Host without depending on the unexported type. The runtime's
// resolveSandboxPolicy does the type assertion.
func NewDefaultSandboxPolicy(workdir string) any {
	p := &sandboxPolicy{
		CWD: workdir,
		// /bin + /sbin matter for bash's literal /bin/sh / /bin/bash
		// argv[0] paths. /tmp + /var/tmp + /run cover scratch space
		// commonly read by plugins. /usr / /lib / /lib64 / /etc /
		// /proc / /dev are bound by the runner unconditionally.
		FSRead:  []string{"/bin", "/sbin", "/tmp", "/var/tmp", "/run"},
		FSWrite: []string{"/tmp", "/var/tmp"},
		// Network: passthrough. Operators wanting deny set "deny"
		// explicitly; per-host allowlists are a future config-driven
		// surface, not the default.
		Net: "allow",
	}
	if workdir != "" {
		p.FSRead = append(p.FSRead, workdir)
		p.FSWrite = append(p.FSWrite, workdir)
	}
	// ssh-agent forwarding, default-on (decision 2026-06-13): when the
	// host has an agent socket, bind it + keep SSH_AUTH_SOCK so
	// git-over-ssh works from a sandboxed bash tool call. Only the
	// socket crosses the boundary — never key bytes. No masking here:
	// this default doesn't bind $HOME, so the key dir is never reachable
	// (masking is the $HOME-bound broker profile's job). No-op when no
	// host agent is present.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		p.Sockets = append(p.Sockets, sock)
		p.Env = append(p.Env, "SSH_AUTH_SOCK")
	}
	return p
}

// sandboxPolicy is the wasm-side wire shape for the optional `sandbox`
// field on stado_exec / stado_proc_spawn requests. Mirrors sandbox.Policy
// but with JSON tags + nil-when-unset semantics (the field is omitempty).
//
// Unsandboxed is the explicit opt-out signal. It's honored ONLY
// when there is no host default to enforce. With a host default set,
// Unsandboxed=true is IGNORED and the host policy still applies.
// This was the security hole flagged in the third-pass review:
// otherwise any plugin author could shrug off host confinement.
//
// Without this field there's no way for the wasm guest to
// distinguish "use host default" from "no sandbox needed" — `null`
// and absent both unmarshal to (*sandboxPolicy)(nil).
type sandboxPolicy struct {
	FSRead      []string `json:"fs_read"`
	FSWrite     []string `json:"fs_write"`
	Exec        []string `json:"exec"`
	Net         string   `json:"net"` // "deny" | "allow" — anything else = unset
	CWD         string   `json:"cwd"`
	Env         []string `json:"env"` // env vars to keep
	Unsandboxed bool     `json:"unsandboxed,omitempty"`
	// Mask names dirs to render unreadable (tmpfs shadow); Sockets names
	// host unix sockets to bind RW (ssh-agent forwarding). Both map 1:1
	// to sandbox.Policy.Mask / .Sockets (decision 2026-06-13). Mask is a
	// restriction (union on intersect); Sockets is an allow (intersect).
	Mask    []string `json:"mask,omitempty"`
	Sockets []string `json:"sockets,omitempty"`
}

// resolveSandboxPolicy picks the effective sandbox policy for a
// stado_exec / stado_proc_spawn call. Host-as-ceiling semantics:
// when the host supplies a default, the guest can only TIGHTEN it —
// a malicious or buggy plugin cannot weaken host policy by claiming
// looser rules. The previous "guest wins" direction was flagged in
// the original 2026-05-09 review and the follow-up consults; this
// commit is the redesign.
//
// Resolution table:
//
//	host=nil  guest=nil               → nil (unsandboxed)
//	host=nil  guest=Unsandboxed=true  → nil (explicit opt-out honored)
//	host=nil  guest non-nil           → guest (no ceiling to enforce)
//	host non-nil  guest=nil           → host (default applies)
//	host non-nil  guest=Unsandboxed=true → host (opt-out IGNORED — host
//	                                      policy is mandatory; if
//	                                      operators want to allow opt-
//	                                      outs they remove the default
//	                                      host-side, not via plugin
//	                                      claim)
//	host non-nil  guest non-nil       → intersect(host, guest)
//	                                      (guest can only narrow)
//
// The Unsandboxed-ignored case is the security-relevant one. With
// "guest wins," any plugin author could shrug off mcp-server's
// auto-confine by setting Unsandboxed=true in their stado_exec args.
// With "host as ceiling," operator policy is the floor; plugin
// claims can only restrict further.
func resolveSandboxPolicy(host *Host, guest *sandboxPolicy) *sandboxPolicy {
	hostPolicy, _ := hostDefaultPolicy(host)
	switch {
	case hostPolicy == nil && guest == nil:
		return nil
	case hostPolicy == nil:
		if guest.Unsandboxed {
			return nil
		}
		return guest
	case guest == nil:
		return hostPolicy
	default:
		// Host non-nil; Unsandboxed is intentionally ignored here.
		return intersectPolicies(hostPolicy, guest)
	}
}

// hostDefaultPolicy extracts the typed *sandboxPolicy from Host's
// any-typed DefaultSandboxPolicy field. Returns (nil, false) when no
// default is set OR when the host wired something that isn't
// *sandboxPolicy (misconfigured entry point — treat as no policy
// rather than panic).
func hostDefaultPolicy(host *Host) (*sandboxPolicy, bool) {
	if host == nil || host.DefaultSandboxPolicy == nil {
		return nil, false
	}
	p, ok := host.DefaultSandboxPolicy.(*sandboxPolicy)
	if !ok {
		return nil, false
	}
	return p, true
}

// intersectPolicies returns the strict intersection of host and guest
// policies — guest can only tighten, never loosen. Per-field
// semantics:
//
//   - FSRead, FSWrite, Exec, Env: result keeps only entries that
//     appear in BOTH lists (path-string equality, no prefix
//     matching). nil-vs-empty-non-nil semantics handled
//     symmetrically for both sides — see intersectStringList for
//     the table.
//   - Net: "deny" wins from either side. host="" is treated as
//     "deny" because the runner's zero-valued NetPolicy translates
//     to NetDenyAll — see intersectNet.
//   - CWD: host wins. Operator chose the workdir; a plugin shouldn't
//     redirect process state into a different directory.
//   - Unsandboxed: ignored — see resolveSandboxPolicy doc.
//
// The function never returns a nil — at least the host's CWD comes
// through. Callers see a real *sandboxPolicy that may be very
// restrictive but is always a valid policy.
func intersectPolicies(host, guest *sandboxPolicy) *sandboxPolicy {
	out := &sandboxPolicy{
		FSRead:  intersectStringList(host.FSRead, guest.FSRead),
		FSWrite: intersectStringList(host.FSWrite, guest.FSWrite),
		Exec:    intersectStringList(host.Exec, guest.Exec),
		Env:     intersectStringList(host.Env, guest.Env),
		Net:     intersectNet(host.Net, guest.Net),
		CWD:     host.CWD,
		// Mask is a restriction: union so a tmpfs shadow either side
		// wants survives (guest can add masks, never remove the host's).
		// Sockets is an allow: intersect like FSRead/Env — guest can
		// only narrow the host's forwarded sockets (a nil guest list
		// inherits the host's, so the default-on agent socket survives
		// for plugins that don't mention sockets).
		Mask:    unionStringList(host.Mask, guest.Mask),
		Sockets: intersectStringList(host.Sockets, guest.Sockets),
	}
	return out
}

// unionStringList returns the deduplicated union of host and guest,
// preserving first-seen order. Used for restriction-class fields (Mask)
// where the safe combine is "everything either side wants hidden."
func unionStringList(host, guest []string) []string {
	if len(host) == 0 && len(guest) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(host)+len(guest))
	var out []string
	for _, s := range append(append([]string{}, host...), guest...) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// intersectStringList returns the field-level intersection of host
// and guest lists with nil-vs-empty semantics that match operator
// intuition:
//
//   - guest=nil → "guest didn't specify" → inherit host's list.
//     Without this, an agent that adds `"net": "deny"` to its
//     stado_exec args would lose ALL of host's FSRead permissions
//     just by being silent on FSRead.
//   - guest=[] (non-nil empty) → "guest explicitly wants nothing"
//     → return nil (no permissions). JSON `[]` unmarshals to a
//     non-nil empty slice, distinct from absent (nil), so callers
//     CAN signal this if they want to lock themselves down.
//   - host=nil + guest non-empty → host has no opinion on this
//     field → guest's list applies (still a tighten relative to
//     "no policy on this field").
//   - both non-empty → strict intersection (only entries in both).
//
// Order follows host's so operators reading the resulting policy
// see their values first.
func intersectStringList(host, guest []string) []string {
	// Symmetric nil-vs-empty handling — `nil` means "no opinion,"
	// non-nil empty means "lock down to nothing." Apply this to BOTH
	// host and guest sides; previously only guest got the explicit-
	// empty interpretation, which let an explicit-empty host
	// (operator deliberately denying everything) be loosened by any
	// guest list. Codex caught it on the fourth pass.
	if guest == nil {
		return host
	}
	if len(guest) == 0 {
		// Guest explicit empty: lock-down. Return non-nil empty so the
		// caller's enforcement gate (`Exec != nil` etc.) treats
		// this as "policy specified, list is empty, deny all"
		// rather than "no policy."
		return []string{}
	}
	if host == nil {
		// Host has no opinion on this field; guest's tighter list
		// applies.
		return guest
	}
	if len(host) == 0 {
		// Host explicit empty: ceiling is "nothing allowed."
		// Intersection with anything = nothing. Return non-nil empty
		// so the runner's `!= nil` enforcement gate triggers.
		return []string{}
	}
	guestSet := make(map[string]struct{}, len(guest))
	for _, g := range guest {
		guestSet[g] = struct{}{}
	}
	out := make([]string, 0, len(host))
	for _, h := range host {
		if _, ok := guestSet[h]; ok {
			out = append(out, h)
		}
	}
	// Non-nil empty when both sides specified non-empty lists with
	// zero overlap. The codex-caught Exec-allows-all bug: previous
	// `len(p.Exec) > 0` enforcement gate couldn't distinguish "nil =
	// no policy" from "[] = deny all," so an intersection-shrunk-to-
	// zero-entries Exec accidentally allowed every binary. Returning
	// non-nil empty + the runner's new `Exec != nil` gate fixes it.
	return out
}

// intersectNet picks the stricter of host and guest, with one
// runner-level subtlety: an empty host string ("") translates to
// NetDenyAll inside buildSandboxedCmd's switch (the zero-valued
// NetPolicy.Kind is NetDenyAll). So host="" effectively MEANS deny
// at the runtime layer; treating it as "no opinion" in the
// intersection would let a guest "allow" loosen the host's de-facto
// deny. Codex caught this on the third pass.
//
// Rules:
//   - host="" → treat as host="deny" for ceiling purposes.
//   - host="deny" OR guest="deny" → "deny" (strictest wins).
//   - host="allow" + guest="allow" → "allow" (both agree).
//   - host="allow" + guest="" → "allow" (host's permissive choice
//     stands; guest didn't specify).
//
// Operators wanting host="" to behave as "no opinion / inherit
// runner default" should explicitly set host="allow" or change the
// runner-side translation. The asymmetry exists because the runner
// today defaults the zero NetPolicy to NetDenyAll.
func intersectNet(host, guest string) string {
	if host == "" {
		// Empty host = de-facto deny at the runner level.
		return "deny"
	}
	if host == "deny" || guest == "deny" {
		return "deny"
	}
	if host == "allow" || guest == "allow" {
		return "allow"
	}
	return ""
}

// buildSandboxedCmd constructs the *exec.Cmd. When policy is nil, runs
// unsandboxed (today's stado_exec semantics). When set, routes through
// sandbox.Detect()'s runner with the supplied policy. If the runner is
// "none" but a non-nil policy was requested, returns an error — silent-
// fall-back-to-unsandboxed would defeat the plugin author's intent.
func buildSandboxedCmd(ctx context.Context, policy *sandboxPolicy, workdir string, argv []string, env []string) (*exec.Cmd, error) {
	return buildSandboxedCmdWithRunner(ctx, sandbox.Detect(), policy, workdir, argv, env)
}

func buildSandboxedCmdWithRunner(ctx context.Context, runner sandbox.Runner, policy *sandboxPolicy, workdir string, argv []string, env []string) (*exec.Cmd, error) {
	if policy == nil {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec
		cmd.Dir = workdir
		if len(env) > 0 {
			cmd.Env = env
		}
		// Don't let a one-shot child grab the host's controlling terminal
		// (ssh/sudo password prompts open /dev/tty directly — see
		// detachControllingTTY).
		detachControllingTTY(cmd)
		return cmd, nil
	}
	// "none" means no runner at all (Linux without bwrap, macOS
	// without sandbox-exec). "windows-passthrough" is the Windows
	// runner that runs unsandboxed because we don't yet have a
	// native confinement story there. Both must hard-fail when a
	// policy was requested — silent fall-back-to-unsandboxed would
	// defeat the plugin author's intent and, worse, give MCP/daemon
	// callers the false impression that confinement is active when
	// it isn't.
	if runner == nil {
		return nil, fmt.Errorf("stado_exec: sandbox policy requested but no sandbox runner was configured")
	}
	if name := runner.Name(); !runner.Available() || name == "none" || name == "windows-passthrough" {
		return nil, fmt.Errorf("stado_exec: sandbox policy requested but no native sandbox runner available on %s (install bubblewrap on Linux or sandbox-exec on macOS; Windows confinement is not yet supported — set sandbox.unsandboxed=true to opt out explicitly)", name)
	}
	p := toSandboxPolicy(policy, workdir)
	cmd, err := runner.Command(ctx, p, argv[0], argv[1:], env)
	if err == nil && cmd != nil {
		// Sandbox wrappers don't always start a new session; detach the
		// controlling tty here too so /dev/tty-grabbing children can't
		// corrupt the TUI through the sandbox.
		detachControllingTTY(cmd)
	}
	return cmd, err
}

func sandboxRunnerForHost(host *Host) sandbox.Runner {
	if host != nil && host.ToolHost != nil {
		if provider, ok := host.ToolHost.(interface{ Runner() sandbox.Runner }); ok {
			if runner := provider.Runner(); runner != nil {
				return runner
			}
		}
	}
	return sandbox.Detect()
}

// toSandboxPolicy translates the wasm-side wire shape into a
// sandbox.Policy. Extracted from buildSandboxedCmd so the field mapping
// — including the ssh-agent passthrough fields Mask/Sockets (decision
// 2026-06-13) — is unit-testable without a runner. CWD defaults to
// workdir when the policy leaves it blank.
func toSandboxPolicy(policy *sandboxPolicy, workdir string) sandbox.Policy {
	cwd := policy.CWD
	if cwd == "" {
		cwd = workdir
	}
	netPolicy := sandbox.NetPolicy{}
	switch policy.Net {
	case "deny":
		netPolicy.Kind = sandbox.NetDenyAll
	case "allow":
		netPolicy.Kind = sandbox.NetAllowAll
	}
	return sandbox.Policy{
		FSRead:  policy.FSRead,
		FSWrite: policy.FSWrite,
		Exec:    policy.Exec,
		Net:     netPolicy,
		CWD:     cwd,
		Env:     policy.Env,
		Mask:    policy.Mask,
		Sockets: policy.Sockets,
	}
}
