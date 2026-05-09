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

	"github.com/foobarto/stado/internal/sandbox"
)

// procHandle holds the state for a long-lived spawned process.
type procHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// procAllowed checks exec:proc / exec:proc:<glob> capability.
//
// Glob forms (EP-no-internal-tools Step 3):
//   - Absolute path: matched against the resolved absolute path
//     (`exec:proc:/usr/bin/bash`, `exec:proc:/usr/bin/impacket-*`)
//   - Slash-free basename: matched against `filepath.Base(resolved)`
//     so cross-distro portability works without hand-tuning manifests
//     (`exec:proc:bash` matches /usr/bin/bash AND /bin/bash)
//   - Mixed forms (relative path with slashes, e.g. `bin/bash`) are
//     rejected as ambiguous.
func (h *Host) procAllowed(bin string) bool {
	if !h.ExecProc {
		return false
	}
	if len(h.ExecProcGlobs) == 0 {
		return true // broad exec:proc
	}
	abs, err := exec.LookPath(bin)
	if err != nil {
		abs = bin
	}
	abs = filepath.Clean(abs)
	base := filepath.Base(abs)
	for _, glob := range h.ExecProcGlobs {
		if strings.Contains(glob, "/") {
			// Absolute-path form (caller responsibility — relative
			// glob with slashes was rejected at cap-parse time).
			if matched, _ := filepath.Match(glob, abs); matched {
				return true
			}
		} else {
			// Slash-free: basename match.
			if matched, _ := filepath.Match(glob, base); matched {
				return true
			}
		}
	}
	return false
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

			cmd, cmdErr := buildSandboxedCmd(execCtx, resolveSandboxPolicy(host, req.Sandbox), host.Workdir, req.Argv, req.Env)
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
			cmd, cmdErr := buildSandboxedCmd(ctx, resolveSandboxPolicy(host, req.Sandbox), host.Workdir, req.Argv, req.Env)
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

// NewDefaultSandboxPolicy returns the host-default sandbox policy for
// entry points that auto-confine stado_exec / stado_proc_spawn calls
// (mcp-server, daemon). The policy is conservatively permissive — runs
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
// Operators wanting tighter rules either (a) supply an explicit
// `sandbox` field on each stado_exec request from the wasm side, or
// (b) opt the plugin OUT of host policy entirely with
// `sandbox: {unsandboxed: true}` — see resolveSandboxPolicy below.
//
// Returns *sandboxPolicy as any so cmd/stado can stash it on a
// tool.Host without depending on the unexported type. The runtime's
// resolveSandboxPolicy does the type assertion.
func NewDefaultSandboxPolicy(workdir string) any {
	return &sandboxPolicy{
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
}

// sandboxPolicy is the wasm-side wire shape for the optional `sandbox`
// field on stado_exec / stado_proc_spawn requests. Mirrors sandbox.Policy
// but with JSON tags + nil-when-unset semantics (the field is omitempty).
//
// Unsandboxed is the explicit opt-out signal: a plugin author who
// knows their tool needs to bypass host policy (rare; mostly debug
// scenarios and bootstrap utilities) sets it true and resolveSandboxPolicy
// returns nil regardless of any host default. Without this field
// there's no way for the wasm guest to distinguish "use host default"
// from "no sandbox needed" — `null` and absent both unmarshal to
// (*sandboxPolicy)(nil).
type sandboxPolicy struct {
	FSRead      []string `json:"fs_read"`
	FSWrite     []string `json:"fs_write"`
	Exec        []string `json:"exec"`
	Net         string   `json:"net"` // "deny" | "allow" — anything else = unset
	CWD         string   `json:"cwd"`
	Env         []string `json:"env"` // env vars to keep
	Unsandboxed bool     `json:"unsandboxed,omitempty"`
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
//   host=nil  guest=nil               → nil (unsandboxed)
//   host=nil  guest=Unsandboxed=true  → nil (explicit opt-out honored)
//   host=nil  guest non-nil           → guest (no ceiling to enforce)
//   host non-nil  guest=nil           → host (default applies)
//   host non-nil  guest=Unsandboxed=true → host (opt-out IGNORED — host
//                                         policy is mandatory; if
//                                         operators want to allow opt-
//                                         outs they remove the default
//                                         host-side, not via plugin
//                                         claim)
//   host non-nil  guest non-nil       → intersect(host, guest)
//                                         (guest can only narrow)
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
//     matching). An empty host list explicitly means "no extra
//     access"; intersect with anything yields empty. Symmetric for
//     guest.
//   - Net: "deny" wins. If either side is "deny" the result is "deny".
//     If host is "allow" and guest is "" the result is "allow"
//     (host's permissiveness; guest didn't pick). If host is "" and
//     guest sets a value, host has no opinion → guest's value applies
//     (only when host policy is otherwise active; this branch is
//     reached only when both are non-nil).
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
	if guest == nil {
		return host
	}
	if len(guest) == 0 {
		// Explicit empty: lock-down. Return non-nil empty so the
		// caller's enforcement gate (`Exec != nil` etc.) treats
		// this as "policy specified, list is empty, deny all"
		// rather than "no policy."
		return []string{}
	}
	if len(host) == 0 {
		return guest
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
	if policy == nil {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec
		cmd.Dir = workdir
		if len(env) > 0 {
			cmd.Env = env
		}
		return cmd, nil
	}
	runner := sandbox.Detect()
	// "none" means no runner at all (Linux without bwrap, macOS
	// without sandbox-exec). "windows-passthrough" is the Windows
	// runner that runs unsandboxed because we don't yet have a
	// native confinement story there. Both must hard-fail when a
	// policy was requested — silent fall-back-to-unsandboxed would
	// defeat the plugin author's intent and, worse, give MCP/daemon
	// callers the false impression that confinement is active when
	// it isn't.
	if name := runner.Name(); name == "none" || name == "windows-passthrough" {
		return nil, fmt.Errorf("stado_exec: sandbox policy requested but no native sandbox runner available on %s (install bubblewrap on Linux or sandbox-exec on macOS; Windows confinement is not yet supported — set sandbox.unsandboxed=true to opt out explicitly)", name)
	}
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
	p := sandbox.Policy{
		FSRead:  policy.FSRead,
		FSWrite: policy.FSWrite,
		Exec:    policy.Exec,
		Net:     netPolicy,
		CWD:     cwd,
		Env:     policy.Env,
	}
	return runner.Command(ctx, p, argv[0], argv[1:], env)
}
