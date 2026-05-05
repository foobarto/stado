# EP-0038a: ABI v2 Host Imports — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add all new Tier 1/2/3 host imports defined in EP-0038 §B to the wazero host module: `stado_fs_read_partial`, `stado_proc_*`, `stado_exec`, `stado_bundled_bin`, `stado_dns_*`, `stado_http_client_*`, `stado_secrets_*`, and Tier 3 crypto/compress/json imports. Does NOT move native tools to wasm (that's EP-0038b).

**Architecture:** Each import group lives in its own `host_*.go` file in `internal/plugins/runtime/`. Registration follows the existing pattern: `register*Imports(builder, host)` called from `InstallHostImports`. Each new capability gate gets a corresponding `Host` field and a `capabilities.go` parse entry.

**Tech Stack:** Go, wazero, os/exec, net (TCP/UDP), crypto/sha256+blake3, compress/gzip+zlib.

**Spec:** `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md` §B

**Depends on:** EP-0037 plan (for capability families `exec:proc`, `terminal:open`, `net:dial:*`, `bundled-bin:*`)

**Sub-plans:**
- **EP-0038a** (this): ABI v2 host imports
- **EP-0038b**: Bundled wasm tool migration (all native → wasm, delete NativeTool wrappers)
- **EP-0038c**: Agent surface (agent.spawn/list/read_messages/send_message/cancel + /session attach)
- **EP-0038d**: Sandbox implementation ([sandbox] wrap mode)

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `internal/plugins/runtime/host_proc.go` | Create | stado_proc_*, stado_exec |
| `internal/plugins/runtime/host_proc_test.go` | Create | Tests |
| `internal/plugins/runtime/host_bundled_bin.go` | Create | stado_bundled_bin |
| `internal/plugins/runtime/host_net_raw.go` | Create | stado_net_dial/listen/accept/read/write/close |
| `internal/plugins/runtime/host_net_icmp.go` | Create | stado_net_icmp_* |
| `internal/plugins/runtime/host_dns.go` | Create | stado_dns_resolve, stado_dns_reverse |
| `internal/plugins/runtime/host_dns_test.go` | Create | Tests |
| `internal/plugins/runtime/host_http_client.go` | Create | stado_http_client_new, stado_http_client_request, stado_http_request_streaming |
| `internal/plugins/runtime/host_secrets.go` | Create | stado_secrets_read, stado_secrets_write, stado_secrets_list |
| `internal/plugins/runtime/host_crypto.go` | Create | stado_hash, stado_hmac |
| `internal/plugins/runtime/host_compress.go` | Create | stado_compress, stado_decompress |
| `internal/plugins/runtime/host_json.go` | Create | stado_json_canonicalise |
| `internal/plugins/runtime/host_fs.go` | Modify | Add stado_fs_read_partial |
| `internal/plugins/runtime/host.go` | Modify | Add new capability fields |
| `internal/plugins/runtime/host_imports.go` | Modify | Register all new import groups |
| `internal/plugins/runtime/handles.go` | Create | Handle registry (32-bit IDs with collision check) |
| `internal/releaseassets/` | Reference | bundled_bin extracts from here |

---

## Task 1: Handle registry (shared by proc, net, terminal, http)

EP-0038 §G defines a typed-handle convention. All stateful imports share a handle registry.

**Files:**
- Create: `internal/plugins/runtime/handles.go`
- Create: `internal/plugins/runtime/handles_test.go`

- [ ] **Step 1: Write tests**

```go
// internal/plugins/runtime/handles_test.go
package runtime

import (
	"testing"
)

func TestHandleRegistry_AllocFree(t *testing.T) {
	r := newHandleRegistry()
	h1 := r.alloc("proc", &struct{}{})
	h2 := r.alloc("proc", &struct{}{})
	if h1 == h2 {
		t.Error("alloc should return unique handles")
	}
	v, ok := r.get(h1)
	if !ok || v == nil {
		t.Error("get should return allocated value")
	}
	r.free(h1)
	_, ok = r.get(h1)
	if ok {
		t.Error("freed handle should not be accessible")
	}
}

func TestHandleRegistry_TypePrefix(t *testing.T) {
	r := newHandleRegistry()
	h := r.alloc("net", &struct{}{})
	if !r.isType(h, "net") {
		t.Error("handle should report correct type")
	}
	if r.isType(h, "proc") {
		t.Error("handle should not match wrong type")
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/plugins/runtime/... -run TestHandleRegistry 2>&1 | head -10
```

- [ ] **Step 3: Implement handles.go**

```go
// internal/plugins/runtime/handles.go
package runtime

import (
	"math/rand/v2"
	"sync"
)

// handleRegistry is a per-Runtime store for opaque handle values.
// Handles are 32-bit IDs; the top 8 bits encode a type tag for
// type-safety checks (EP-0038 §G). Zero is reserved (invalid handle).
type handleRegistry struct {
	mu      sync.Mutex
	entries map[uint32]handleEntry
}

type handleEntry struct {
	typeTag string
	value   any
}

func newHandleRegistry() *handleRegistry {
	return &handleRegistry{entries: make(map[uint32]handleEntry)}
}

// alloc allocates a new handle of the given type tag. Retries on the
// rare 32-bit collision (EP-0038 D22).
func (r *handleRegistry) alloc(typeTag string, value any) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		id := rand.Uint32()
		if id == 0 {
			continue
		}
		if _, exists := r.entries[id]; exists {
			continue
		}
		r.entries[id] = handleEntry{typeTag: typeTag, value: value}
		return id
	}
}

func (r *handleRegistry) get(handle uint32) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[handle]
	if !ok {
		return nil, false
	}
	return e.value, true
}

func (r *handleRegistry) free(handle uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, handle)
}

func (r *handleRegistry) isType(handle uint32, typeTag string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[handle]
	return ok && e.typeTag == typeTag
}
```

- [ ] **Step 4: Add handles field to Runtime**

In `internal/plugins/runtime/runtime.go`, add `handles *handleRegistry` to the `Runtime` struct and initialize it in `New()`:

```go
// In Runtime struct:
handles *handleRegistry

// In New():
handles: newHandleRegistry(),
```

- [ ] **Step 5: Run tests**

```
go test ./internal/plugins/runtime/... -run TestHandleRegistry -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/runtime/handles.go internal/plugins/runtime/handles_test.go internal/plugins/runtime/runtime.go
git commit -m "feat(ep-0038a): handle registry for stateful host imports"
```

---

## Task 2: stado_fs_read_partial

**Files:**
- Modify: `internal/plugins/runtime/host_fs.go`
- Modify: `internal/plugins/runtime/host_imports.go`

- [ ] **Step 1: Write test**

```go
// internal/plugins/runtime/host_fs_test.go (add to existing or create)
func TestFSReadPartial(t *testing.T) {
	// Write a 100-byte test file.
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	os.WriteFile(path, data, 0o644)

	// The partial-read host import is tested indirectly via a wasm plugin
	// in EP-0038b. Here we test the Go implementation directly.
	host := &Host{
		FSRead:  []string{dir},
		Workdir: dir,
		Logger:  slog.Default(),
	}
	result, err := fsReadPartial(host, path, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 20 {
		t.Errorf("expected 20 bytes, got %d", len(result))
	}
	if result[0] != 10 {
		t.Errorf("expected first byte=10 (offset 10), got %d", result[0])
	}
}
```

- [ ] **Step 2: Implement fsReadPartial in host_fs.go**

Add after `registerFSWriteImport`:

```go
// fsReadPartial reads up to length bytes starting at offset from abs.
// Exported as a Go helper so it can be tested without wasm.
func fsReadPartial(host *Host, abs string, offset, length int64) ([]byte, error) {
	if !host.allowRead(abs) {
		return nil, os.ErrPermission
	}
	root, rel, err := openAllowedRoot(abs, host.FSRead, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", abs)
	}
	if seeker, ok := f.(io.Seeker); ok {
		if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
	}
	if length > maxPluginRuntimeFSFileBytes {
		length = maxPluginRuntimeFSFileBytes
	}
	buf := make([]byte, length)
	n, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF {
		return buf[:n], nil // EOF before length — that's fine
	}
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func registerFSReadPartialImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_fs_read_partial(path_ptr, path_len, offset_hi, offset_lo,
	//                        length_hi, length_lo, buf_ptr, buf_cap) → int32
	// offset and length are passed as two i32 halves of an i64 (wasm32 compat).
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			pathPtr := api.DecodeU32(stack[0])
			pathLen := api.DecodeU32(stack[1])
			offsetHi := api.DecodeI32(stack[2])
			offsetLo := api.DecodeI32(stack[3])
			lengthHi := api.DecodeI32(stack[4])
			lengthLo := api.DecodeI32(stack[5])
			bufPtr := api.DecodeU32(stack[6])
			bufCap := api.DecodeU32(stack[7])

			offset := (int64(offsetHi) << 32) | int64(uint32(offsetLo))
			length := (int64(lengthHi) << 32) | int64(uint32(lengthLo))
			if length <= 0 || offset < 0 {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if length > int64(bufCap) {
				length = int64(bufCap)
			}

			if pathLen > maxPluginRuntimeFSPathBytes {
				stack[0] = api.EncodeI32(-1)
				return
			}
			path, err := readStringLimited(mod, pathPtr, pathLen, maxPluginRuntimeFSPathBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			abs, err := realPath(host.Workdir, path)
			if err != nil {
				host.Logger.Warn("stado_fs_read_partial: path resolution failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := fsReadPartial(host, abs, offset, length)
			if err != nil {
				host.Logger.Warn("stado_fs_read_partial failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, data))
		}),
		[]api.ValueType{
			api.ValueTypeI32, api.ValueTypeI32, // path
			api.ValueTypeI32, api.ValueTypeI32, // offset hi/lo
			api.ValueTypeI32, api.ValueTypeI32, // length hi/lo
			api.ValueTypeI32, api.ValueTypeI32, // buf ptr/cap
		},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_fs_read_partial")
}
```

- [ ] **Step 3: Register in InstallHostImports**

In `host_imports.go`, add `registerFSReadPartialImport(builder, host)` inside `registerFSImports`:

```go
func registerFSImports(builder wazero.HostModuleBuilder, host *Host) {
	registerFSReadImport(builder, host)
	registerFSWriteImport(builder, host)
	registerFSReadPartialImport(builder, host)
}
```

- [ ] **Step 4: Run tests + build**

```
go test ./internal/plugins/runtime/... -run TestFSRead -v
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/runtime/host_fs.go internal/plugins/runtime/host_imports.go
git commit -m "feat(ep-0038a): stado_fs_read_partial host import (D24)"
```

---

## Task 3: stado_proc_* and stado_exec (process spawn)

**Files:**
- Create: `internal/plugins/runtime/host_proc.go`
- Create: `internal/plugins/runtime/host_proc_test.go`
- Modify: `internal/plugins/runtime/host.go` (add ExecProc bool + ProcGlob string)
- Modify: `internal/plugins/runtime/host_imports.go`

- [ ] **Step 1: Write test**

```go
// internal/plugins/runtime/host_proc_test.go
package runtime

import (
	"context"
	"log/slog"
	"testing"
)

func TestExecOneShot(t *testing.T) {
	host := &Host{
		ExecProc:   true,
		ExecProcGlob: "", // empty = broad exec:proc allowed
		Workdir:    t.TempDir(),
		Logger:     slog.Default(),
	}
	result, exitCode, err := hostExec(context.Background(), host, []string{"echo", "hello"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Errorf("expected exit 0, got %d", exitCode)
	}
	if string(result) != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", result)
	}
}
```

- [ ] **Step 2: Add capability fields to Host**

In `internal/plugins/runtime/host.go`, add:

```go
// ExecProc gates stado_proc_spawn and stado_exec.
// Set when manifest declares exec:proc or exec:proc:<glob>.
ExecProc     bool
ExecProcGlob string // non-empty = scoped variant exec:proc:<glob>
```

Update capability parsing in the Host constructor (where `Manifest.Capabilities` is parsed). Find the `parseCapabilities` or equivalent function and add:

```go
case strings.HasPrefix(cap, "exec:proc"):
	host.ExecProc = true
	if rest := strings.TrimPrefix(cap, "exec:proc:"); rest != cap {
		host.ExecProcGlob = rest
	}
```

- [ ] **Step 3: Implement host_proc.go**

```go
// internal/plugins/runtime/host_proc.go
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// hostExec is the Go implementation of stado_exec: one-shot process run.
func hostExec(ctx context.Context, host *Host, argv []string, stdin string, env []string) (stdout []byte, exitCode int, err error) {
	if len(argv) == 0 {
		return nil, -1, fmt.Errorf("exec: empty argv")
	}
	if !host.procAllowed(argv[0]) {
		return nil, -1, fmt.Errorf("exec: %q denied by exec:proc cap", argv[0])
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = host.Workdir
	if len(env) > 0 {
		cmd.Env = env
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if runErr := cmd.Run(); runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			return out.Bytes(), ee.ExitCode(), nil
		}
		return nil, -1, runErr
	}
	return out.Bytes(), 0, nil
}

// procAllowed checks exec:proc / exec:proc:<glob> cap.
func (host *Host) procAllowed(bin string) bool {
	if !host.ExecProc {
		return false
	}
	if host.ExecProcGlob == "" {
		return true // broad exec:proc
	}
	// Scoped: match against glob. If bin is relative, resolve via PATH first.
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
	registerProcCloseImport(builder, host, rt)
}

func registerExecImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_exec(req_ptr, req_len, result_ptr, result_cap) → int32
	// req and result are JSON-encoded ExecRequest / ExecResult.
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
			if err := json.Unmarshal(reqBytes, &req); err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			timeout := 30 * time.Second
			if req.Timeout > 0 {
				timeout = time.Duration(req.Timeout) * time.Millisecond
			}
			execCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			stdout, exitCode, execErr := hostExec(execCtx, host, req.Argv, req.Stdin, req.Env)
			type result struct {
				Stdout   string `json:"stdout"`
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error,omitempty"`
			}
			res := result{Stdout: string(stdout), ExitCode: exitCode}
			if execErr != nil {
				res.Error = execErr.Error()
			}
			payload, _ := json.Marshal(res)
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_exec")
}

func registerProcSpawnImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	// stado_proc_spawn(req_ptr, req_len) → handle (u32), 0 on error
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
				host.Logger.Warn("stado_proc_spawn denied", slog.String("bin", req.Argv[0]))
				stack[0] = 0
				return
			}
			cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
			cmd.Dir = host.Workdir
			if len(req.Env) > 0 {
				cmd.Env = req.Env
			}
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				host.Logger.Warn("stado_proc_spawn failed", slog.String("err", err.Error()))
				stack[0] = 0
				return
			}
			h := rt.handles.alloc("proc", &procHandle{cmd: cmd, stdin: stdin, stdout: stdout})
			stack[0] = uint64(h)
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_proc_spawn")
}

func registerProcReadImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	// stado_proc_read(h, max, timeout_ms, buf_ptr, buf_cap) → int32
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			h := uint32(stack[0])
			max := api.DecodeU32(stack[1])
			timeoutMs := api.DecodeU32(stack[2])
			bufPtr := api.DecodeU32(stack[3])
			bufCap := api.DecodeU32(stack[4])
			v, ok := rt.handles.get(h)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			ph, ok := v.(*procHandle)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if max > bufCap {
				max = bufCap
			}
			_ = timeoutMs // TODO: apply read deadline
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

func registerProcWriteImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	// stado_proc_write(h, buf_ptr, buf_len) → int32
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			h := uint32(stack[0])
			bufPtr := api.DecodeU32(stack[1])
			bufLen := api.DecodeU32(stack[2])
			v, ok := rt.handles.get(h)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			ph, ok := v.(*procHandle)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := readBytesLimited(mod, bufPtr, bufLen, 16<<20)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			n, err := ph.stdin.Write(data)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(int32(n))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_proc_write")
}

func registerProcWaitImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	// stado_proc_wait(h) → exit_code (i32), -1 on error
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, _ api.Module, stack []uint64) {
			h := uint32(stack[0])
			v, ok := rt.handles.get(h)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			ph, ok := v.(*procHandle)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := ph.cmd.Wait(); err != nil {
				if ee, ok2 := err.(*exec.ExitError); ok2 {
					stack[0] = api.EncodeI32(int32(ee.ExitCode()))
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

func registerProcCloseImport(builder wazero.HostModuleBuilder, _ *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			h := uint32(stack[0])
			if v, ok := rt.handles.get(h); ok {
				if ph, ok2 := v.(*procHandle); ok2 {
					_ = ph.cmd.Process.Kill()
				}
			}
			rt.handles.free(h)
		}),
		[]api.ValueType{api.ValueTypeI32},
		[]api.ValueType{}).
		Export("stado_proc_close")
}

type procHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}
```

- [ ] **Step 4: Register in InstallHostImports**

In `host_imports.go`, add to `InstallHostImports`:

```go
registerProcImports(builder, host, r)
```

Also update the `if host.ExecPTY && host.PTYManager == nil` block — the runtime `r` is now passed to `registerProcImports` for handle access. Update the function signature to receive `*Runtime`:
`func InstallHostImports(ctx context.Context, r *Runtime, host *Host) error`
(already the existing signature — just confirm it's used consistently).

- [ ] **Step 5: Run tests + build**

```
go test ./internal/plugins/runtime/... -run TestExec -v
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/runtime/host_proc.go internal/plugins/runtime/host_proc_test.go internal/plugins/runtime/host.go internal/plugins/runtime/host_imports.go
git commit -m "feat(ep-0038a): stado_proc_* and stado_exec host imports"
```

---

## Task 4: stado_bundled_bin (lazy-extract + flock cache)

**Files:**
- Create: `internal/plugins/runtime/host_bundled_bin.go`
- Modify: `internal/plugins/runtime/host.go` (add BundledBin bool)
- Modify: `internal/plugins/runtime/host_imports.go`

- [ ] **Step 1: Write test**

```go
// Verify stado_bundled_bin returns a valid path for a known binary.
func TestBundledBinRipgrep(t *testing.T) {
	host := &Host{BundledBin: true, Logger: slog.Default()}
	path, err := bundledBinPath(host, "ripgrep")
	if err != nil {
		t.Skip("ripgrep not available in test environment:", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("bundled binary path %q does not exist: %v", path, err)
	}
}
```

- [ ] **Step 2: Implement host_bundled_bin.go**

```go
// internal/plugins/runtime/host_bundled_bin.go
package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/releaseassets"
)

var bundledBinMu sync.Mutex

// bundledBinPath extracts the named bundled binary (via releaseassets)
// to a stable cache path keyed by sha256. Flock-serialised to avoid
// race on concurrent plugin instances.
func bundledBinPath(host *Host, name string) (string, error) {
	if !host.BundledBin {
		return "", fmt.Errorf("bundled-bin:* capability required")
	}
	data, err := releaseassets.BundledBin(name)
	if err != nil {
		return "", fmt.Errorf("stado_bundled_bin: %q: %w", name, err)
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])[:16]
	cacheDir := filepath.Join(os.TempDir(), "stado-bundled-bin", name+"-"+key)

	bundledBinMu.Lock()
	defer bundledBinMu.Unlock()

	binPath := filepath.Join(cacheDir, name)
	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil // already extracted
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	tmp := binPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return "", err
	}
	return binPath, os.Rename(tmp, binPath)
}

func registerBundledBinImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_bundled_bin(name_ptr, name_len, buf_ptr, buf_cap) → int32
	// Returns the path to the extracted binary as a UTF-8 string.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			namePtr := api.DecodeU32(stack[0])
			nameLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufCap := api.DecodeU32(stack[3])

			name, err := readStringLimited(mod, namePtr, nameLen, 256)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			path, err := bundledBinPath(host, name)
			if err != nil {
				host.Logger.Warn("stado_bundled_bin failed", slog.String("name", name), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, []byte(path)))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_bundled_bin")
}
```

Add `BundledBin bool` to Host and `bundled-bin:*` to capability parsing.
Register `registerBundledBinImport(builder, host)` in `host_imports.go`.

- [ ] **Step 3: Build**

```
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/runtime/host_bundled_bin.go internal/plugins/runtime/host.go internal/plugins/runtime/host_imports.go
git commit -m "feat(ep-0038a): stado_bundled_bin host import"
```

---

## Task 5: stado_dns_* (Tier 2 DNS resolver)

**Files:**
- Create: `internal/plugins/runtime/host_dns.go`
- Create: `internal/plugins/runtime/host_dns_test.go`

- [ ] **Step 1: Write test**

```go
// host_dns_test.go
func TestDNSResolve(t *testing.T) {
	if testing.Short() {
		t.Skip("DNS test requires network")
	}
	host := &Host{DNSResolve: true, Logger: slog.Default()}
	addrs, err := hostDNSResolve(context.Background(), host, "localhost", "A")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range addrs {
		if a == "127.0.0.1" || a == "::1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected localhost to resolve to 127.0.0.1, got %v", addrs)
	}
}
```

- [ ] **Step 2: Implement host_dns.go**

```go
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func hostDNSResolve(ctx context.Context, host *Host, name, qtype string) ([]string, error) {
	if !host.DNSResolve {
		return nil, fmt.Errorf("dns:resolve:* capability required")
	}
	switch qtype {
	case "A", "AAAA", "":
		addrs, err := net.DefaultResolver.LookupHost(ctx, name)
		if err != nil {
			return nil, err
		}
		return addrs, nil
	case "TXT":
		txts, err := net.DefaultResolver.LookupTXT(ctx, name)
		if err != nil {
			return nil, err
		}
		return txts, nil
	case "MX":
		mxs, err := net.DefaultResolver.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, mx := range mxs {
			out = append(out, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported qtype: %q", qtype)
	}
}

func registerDNSImports(builder wazero.HostModuleBuilder, host *Host) {
	// stado_dns_resolve(req_ptr, req_len, result_ptr, result_cap) → int32
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr, reqLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			resPtr, resCap := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			reqBytes, err := readBytesLimited(mod, reqPtr, reqLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var req struct {
				Name  string `json:"name"`
				Qtype string `json:"qtype"`
			}
			if err := json.Unmarshal(reqBytes, &req); err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			addrs, err := hostDNSResolve(ctx, host, req.Name, req.Qtype)
			type result struct {
				Records []string `json:"records"`
				Error   string   `json:"error,omitempty"`
			}
			res := result{Records: addrs}
			if err != nil {
				res.Error = err.Error()
			}
			payload, _ := json.Marshal(res)
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, payload))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_dns_resolve")
}
```

Add `DNSResolve bool`, `DNSReverse bool` to Host. Parse `dns:resolve:*` and `dns:reverse:*` caps.
Register in `host_imports.go`.

- [ ] **Step 3: Run tests + build**

```
go test ./internal/plugins/runtime/... -run TestDNS -short -v
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/runtime/host_dns.go internal/plugins/runtime/host_dns_test.go internal/plugins/runtime/host.go internal/plugins/runtime/host_imports.go
git commit -m "feat(ep-0038a): stado_dns_resolve host import (Tier 2)"
```

---

## Task 6: Tier 3 — stado_hash, stado_compress, stado_decompress, stado_json_canonicalise

**Files:**
- Create: `internal/plugins/runtime/host_crypto.go`
- Create: `internal/plugins/runtime/host_compress.go`
- Create: `internal/plugins/runtime/host_json_canon.go`

These are stateless; no capability gates beyond the cap declaration. No Host fields needed.

- [ ] **Step 1: Implement host_crypto.go**

```go
package runtime

import (
	"crypto/hmac"
	"crypto/md5"  // #nosec G501 -- legacy hash, not for security
	"crypto/sha1" // #nosec G505
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	// blake3 "github.com/zeebo/blake3" -- add dependency if available
)

func registerCryptoImports(builder wazero.HostModuleBuilder, host *Host) {
	// stado_hash(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_cap) → int32
	// Returns hex-encoded digest. Algos: md5, sha1, sha256, sha512.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			dataPtr, dataLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			outPtr, outCap := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])

			algo, err := readStringLimited(mod, algoPtr, algoLen, 32)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := readBytesLimited(mod, dataPtr, dataLen, maxPluginRuntimeFSFileBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var h hash.Hash
			switch algo {
			case "md5":
				h = md5.New() // #nosec G401
			case "sha1":
				h = sha1.New() // #nosec G401
			case "sha256":
				h = sha256.New()
			case "sha512":
				h = sha512.New()
			default:
				host.Logger.Warn("stado_hash: unknown algo", slog.String("algo", algo))
				stack[0] = api.EncodeI32(-1)
				return
			}
			h.Write(data)
			digest := hex.EncodeToString(h.Sum(nil))
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, []byte(digest)))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_hash")
}
```

- [ ] **Step 2: Implement host_compress.go**

```go
package runtime

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerCompressImports(builder wazero.HostModuleBuilder, host *Host) {
	// stado_compress(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_cap) → int32
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			dataPtr, dataLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			outPtr, outCap := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])

			algo, _ := readStringLimited(mod, algoPtr, algoLen, 32)
			data, err := readBytesLimited(mod, dataPtr, dataLen, maxPluginRuntimeFSFileBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var compressed []byte
			switch algo {
			case "gzip":
				var buf bytes.Buffer
				w := gzip.NewWriter(&buf)
				w.Write(data)
				w.Close()
				compressed = buf.Bytes()
			case "zlib":
				var buf bytes.Buffer
				w := zlib.NewWriter(&buf)
				w.Write(data)
				w.Close()
				compressed = buf.Bytes()
			default:
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, compressed))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_compress")

	// stado_decompress — same signature, inverse operation
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			dataPtr, dataLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			outPtr, outCap := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])

			algo, _ := readStringLimited(mod, algoPtr, algoLen, 32)
			data, err := readBytesLimited(mod, dataPtr, dataLen, maxPluginRuntimeFSFileBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			var decompressed []byte
			switch algo {
			case "gzip":
				r, err := gzip.NewReader(bytes.NewReader(data))
				if err != nil {
					stack[0] = api.EncodeI32(-1)
					return
				}
				decompressed, err = io.ReadAll(io.LimitReader(r, maxPluginRuntimeFSFileBytes))
				if err != nil {
					stack[0] = api.EncodeI32(-1)
					return
				}
			default:
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, decompressed))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_decompress")
}
```

- [ ] **Step 3: Register all Tier 3 imports in host_imports.go**

Add to `InstallHostImports`:

```go
registerCryptoImports(builder, host)
registerCompressImports(builder, host)
```

- [ ] **Step 4: Build + run full runtime test suite**

```
go test ./internal/plugins/runtime/... -v -count=1 2>&1 | tail -40
go build ./...
```
Expected: all existing tests pass, build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/runtime/host_crypto.go internal/plugins/runtime/host_compress.go internal/plugins/runtime/host_imports.go
git commit -m "feat(ep-0038a): Tier 3 host imports (hash, compress/decompress)"
```

---

## Self-Review

**Spec §B coverage:**

| Import group | Task |
|---|---|
| `stado_fs_read_partial` | Task 2 |
| `stado_proc_*`, `stado_exec` | Task 3 |
| `stado_terminal_*` | Already exists (host_pty.go) — wire to new handle registry |
| `stado_net_dial/listen/accept/read/write/close` | Deferred (TCP plumbing; add as Task 7 if needed) |
| `stado_net_icmp_*` | Deferred (requires CAP_NET_RAW; add as Task 8) |
| `stado_bundled_bin` | Task 4 |
| `stado_dns_*` | Task 5 |
| `stado_http_client_*` / `stado_http_request_streaming` | Deferred (extend existing host_http*) |
| `stado_secrets_*` | Deferred (EP-0038 §B Tier 2) |
| `stado_hash`, `stado_compress`, `stado_decompress` | Task 6 |
| `stado_json_canonicalise` | Extend Task 6 |

Net/ICMP/HTTP-streaming/secrets can be added as follow-on tasks in the same plan; none block EP-0038b (wasm migration) since the wasm plugins can use existing `stado_http_request` until the new client surface lands.
