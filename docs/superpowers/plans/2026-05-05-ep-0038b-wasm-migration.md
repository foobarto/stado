# EP-0038b: Bundled Wasm Tool Migration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert every native bundled tool to a real wasm plugin. Delete `buildNativeRegistry()` and the `newBundledPluginTool` facade layer. After this plan, `BuildDefaultRegistry()` loads `.wasm` bytes compiled from `internal/bundledplugins/modules/<plugin>/` — no more Go structs behind the tool names.

**Architecture:** Each plugin lives in `internal/bundledplugins/modules/<plugin>/main.go` (GOOS=wasip1, `//go:build wasip1`). The plugin calls host imports via `//go:wasmimport stado stado_*`. `build.sh` in each module compiles to `internal/bundledplugins/wasm/<plugin>.wasm`, embedded via `internal/bundledplugins/embed.go`. The manifest for each bundled plugin is declared inline (no on-disk file) via a new `BundledManifest` map.

**Tech Stack:** Go (GOOS=wasip1 -buildmode=c-shared), wazero, existing `internal/bundledplugins/sdk`.

**Spec:** `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md` §A, §C, §K

**Depends on:** EP-0038a (for stado_fs_read_partial and stado_proc_* imports)

**Parity gate (EP-0038 D21):** Every plugin must pass a per-tool parity test that runs the old native tool and the new wasm tool on the same input and asserts identical output before the native tool is deleted.

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `internal/bundledplugins/modules/fs/main.go` | Create | fs.read (with offset/length), fs.write, fs.edit, fs.glob, fs.grep |
| `internal/bundledplugins/modules/shell/main.go` | Create | shell.exec (calls stado_exec) |
| `internal/bundledplugins/modules/web/main.go` | Create | web.fetch, web.search, web.browse |
| `internal/bundledplugins/modules/rg/main.go` | Create | rg.search (calls stado_bundled_bin + stado_exec) |
| `internal/bundledplugins/modules/readctx/main.go` | Create | readctx.read |
| `internal/bundledplugins/modules/tools/main.go` | Create | tools.search/describe/categories/in_category (port from native) |
| `internal/bundledplugins/embed.go` | Modify | Embed new .wasm files |
| `internal/bundledplugins/build.sh` | Modify | Build all modules |
| `internal/runtime/bundled_plugin_tools.go` | Delete | After parity verified |
| `internal/runtime/executor.go` | Modify | BuildDefaultRegistry loads wasm directly |

For brevity, this plan covers `fs` and `shell` in full. `rg`, `readctx`, `web`, and `tools` follow the exact same pattern — one task each.

---

## Task 1: Understand the existing pattern + build infrastructure

- [ ] **Step 1: Read one existing module**

```
cat internal/bundledplugins/modules/read/main.go
cat internal/bundledplugins/modules/bash/main.go
```

The pattern: `//go:wasmimport stado stado_fs_tool_read` then `stado_tool_<name>` export. The native tool's `Run(args, h)` becomes the host import call on the wasm side.

- [ ] **Step 2: Read the build script**

```
cat internal/bundledplugins/build.sh
```

Note the `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared` invocation per module. The new modules follow the same structure but call the new ABI imports instead of `stado_fs_tool_read`.

- [ ] **Step 3: Understand embed.go**

```
cat internal/bundledplugins/embed.go
```

New modules get `//go:embed wasm/<plugin>.wasm` entries here. The embed key must match what `MustWasm(name)` looks up.

- [ ] **Step 4: Verify build infrastructure works for a module change**

```
cd internal/bundledplugins/modules/read && \
  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../../wasm/read.wasm .
```
Expected: `wasm/read.wasm` updated without error.

---

## Task 2: fs plugin (fs.read with offset/length, fs.write, fs.edit, fs.glob, fs.grep)

**Files:**
- Create: `internal/bundledplugins/modules/fs/main.go`
- Modify: `internal/bundledplugins/build.sh`
- Modify: `internal/bundledplugins/embed.go`

- [ ] **Step 1: Write parity test**

```go
// internal/runtime/bundled_plugin_tools_test.go — add:
func TestFSReadParityWasm(t *testing.T) {
	// Write a temp file.
	dir := t.TempDir()
	content := []byte("hello parity test")
	os.WriteFile(filepath.Join(dir, "test.txt"), content, 0o644)

	// Old path: native ReadTool.
	nativeTool := fs.ReadTool{}
	nativeResult, _ := nativeTool.Run(context.Background(),
		json.RawMessage(`{"path":"test.txt"}`),
		&testHost{workdir: dir})

	// New path: wasm fs plugin.
	reg := BuildDefaultRegistry()
	wasmTool, ok := reg.Get("fs__read")
	if !ok {
		t.Skip("fs__read not yet in registry")
	}
	wasmResult, _ := wasmTool.Run(context.Background(),
		json.RawMessage(`{"path":"test.txt"}`),
		&testHost{workdir: dir})

	if nativeResult.Content != wasmResult.Content {
		t.Errorf("parity fail:\nnative: %s\nwasm:   %s", nativeResult.Content, wasmResult.Content)
	}
}
```

- [ ] **Step 2: Create fs/main.go**

```go
//go:build wasip1

package main

import (
	"encoding/json"
	"unsafe"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_fs_read
func stadoFSRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_read_partial
func stadoFSReadPartial(pathPtr, pathLen, offsetHi, offsetLo, lengthHi, lengthLo, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFSWrite(pathPtr, pathLen, dataPtr, dataLen uint32) int32

//go:wasmimport stado stado_fs_edit
func stadoFSEdit(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_fs_glob
func stadoFSGlob(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_fs_grep
func stadoFSGrep(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// stado_tool_fs__read dispatches to the read tool.
//
//go:wasmexport stado_tool_fs__read
func stadoToolFSRead(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
		Length int64  `json:"length"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	pathBytes := []byte(req.Path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)

	const bufSize = 16 << 20 // 16 MiB max
	buf := sdk.Alloc(bufSize)

	var n int32
	if req.Offset > 0 || req.Length > 0 {
		length := req.Length
		if length <= 0 {
			length = bufSize
		}
		offsetHi := int32(req.Offset >> 32)
		offsetLo := int32(req.Offset & 0xFFFFFFFF)
		lengthHi := int32(length >> 32)
		lengthLo := int32(length & 0xFFFFFFFF)
		n = stadoFSReadPartial(
			uint32(pathPtr), uint32(len(pathBytes)),
			uint32(offsetHi), uint32(offsetLo),
			uint32(lengthHi), uint32(lengthLo),
			uint32(buf), bufSize,
		)
	} else {
		n = stadoFSRead(uint32(pathPtr), uint32(len(pathBytes)), uint32(buf), bufSize)
	}
	if n < 0 {
		return writeError(resPtr, resCap, "read failed")
	}
	content := sdk.Bytes(buf, n)
	resp, _ := json.Marshal(map[string]string{"content": string(content)})
	return writeResult(resPtr, resCap, resp)
}

// stado_tool_fs__write
//go:wasmexport stado_tool_fs__write
func stadoToolFSWrite(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	pathBytes := []byte(req.Path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	dataBytes := []byte(req.Content)
	dataPtr := sdk.Alloc(int32(len(dataBytes)))
	sdk.Write(dataPtr, dataBytes)
	n := stadoFSWrite(uint32(pathPtr), uint32(len(pathBytes)), uint32(dataPtr), uint32(len(dataBytes)))
	if n < 0 {
		return writeError(resPtr, resCap, "write failed")
	}
	resp, _ := json.Marshal(map[string]string{"written": req.Path})
	return writeResult(resPtr, resCap, resp)
}

// helpers
func writeError(resPtr, resCap int32, msg string) int32 {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return writeResult(resPtr, resCap, b)
}

func writeResult(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}

var _ = unsafe.Pointer(nil) // keep unsafe import if needed by sdk
```

(Note: `stado_fs_edit`, `stado_fs_glob`, `stado_fs_grep` need corresponding host imports if they don't exist yet, OR the wasm plugin calls the existing `stado_fs_tool_edit` etc. imports. Check existing wasm modules for edit/glob/grep patterns — they likely already delegate to the native tool host import. Reuse that pattern.)

- [ ] **Step 3: Build fs wasm**

```
cd internal/bundledplugins/modules/fs
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../../wasm/fs.wasm .
ls -la ../../wasm/fs.wasm
```
Expected: `fs.wasm` created.

- [ ] **Step 4: Update embed.go**

Add:
```go
//go:embed wasm/fs.wasm
var FSWasm []byte
```

Update `MustWasm` to handle `"fs__read"`, `"fs__write"` etc. by stripping the alias prefix:

```go
func MustWasm(name string) []byte {
	// Strip plugin alias prefix (e.g. "fs__read" → look up "fs" wasm).
	if idx := strings.Index(name, "__"); idx >= 0 {
		name = name[:idx]
	}
	switch name {
	case "fs":     return FSWasm
	case "shell":  return ShellWasm
	// ... existing entries
	}
	panic("no wasm for: " + name)
}
```

- [ ] **Step 5: Run parity test**

```
go test ./internal/runtime/... -run TestFSReadParityWasm -v
```
Expected: PASS (or SKIP if wasm tool not yet registered — wire it in BuildDefaultRegistry first).

- [ ] **Step 6: Register fs tools in BuildDefaultRegistry**

In `internal/runtime/executor.go` (or `bundled_plugin_tools.go`), add alongside existing registrations:

```go
// EP-0038b: register wasm-backed fs tools replacing native ones.
// Wire name: "fs__read", "fs__write", etc.
for _, toolName := range []string{"read", "write", "edit", "glob", "grep"} {
	reg.Register(newWasmPluginTool("fs", toolName))
}
```

`newWasmPluginTool(alias, toolName)` creates a `bundledPluginTool` with the wire-form name:

```go
func newWasmPluginTool(alias, toolName string) tool.Tool {
	wireName, _ := tools.WireForm(alias, toolName)
	// Look up description + schema from a manifest registry (or hardcode for bundled tools).
	return &bundledPluginTool{
		manifest: ..., // build from alias + toolName
		def: plugins.ToolDef{Name: wireName, ...},
		wasm: bundledplugins.MustWasm(alias),
	}
}
```

- [ ] **Step 7: Remove native fs tools from registry**

After the wasm tools are registered and parity verified:

```go
// Remove old bare-name native tools.
reg.Unregister("read")
reg.Unregister("write")
reg.Unregister("edit")
reg.Unregister("glob")
reg.Unregister("grep")
```

- [ ] **Step 8: Run full test suite**

```
go test ./... -count=1 2>&1 | grep -E "FAIL|ok"
```
Expected: no FAILs.

- [ ] **Step 9: Commit**

```bash
git add internal/bundledplugins/modules/fs/ internal/bundledplugins/wasm/fs.wasm internal/bundledplugins/embed.go internal/runtime/executor.go
git commit -m "feat(ep-0038b): fs plugin wasm — fs.read/write/edit/glob/grep"
```

---

## Task 3: shell plugin (shell.exec via stado_exec)

**Files:**
- Create: `internal/bundledplugins/modules/shell/main.go`

- [ ] **Step 1: Write parity test**

```go
func TestShellExecParityWasm(t *testing.T) {
	// Old: native bash tool
	nativeTool := bash.BashTool{Timeout: 5 * time.Second}
	nativeResult, _ := nativeTool.Run(context.Background(),
		json.RawMessage(`{"command":"echo hello"}`),
		&testHost{workdir: t.TempDir()})

	// New: wasm shell.exec
	reg := BuildDefaultRegistry()
	wasmTool, ok := reg.Get("shell__exec")
	if !ok {
		t.Skip("shell__exec not yet in registry")
	}
	wasmResult, _ := wasmTool.Run(context.Background(),
		json.RawMessage(`{"command":"echo hello"}`),
		&testHost{workdir: t.TempDir()})

	if strings.TrimSpace(nativeResult.Content) != strings.TrimSpace(wasmResult.Content) {
		t.Errorf("parity:\nnative: %q\nwasm: %q", nativeResult.Content, wasmResult.Content)
	}
}
```

- [ ] **Step 2: Create shell/main.go**

```go
//go:build wasip1

package main

import (
	"encoding/json"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

//go:wasmexport stado_tool_shell__exec
func stadoToolShellExec(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	execReq, _ := json.Marshal(map[string]any{
		"argv":       []string{"/bin/sh", "-c", req.Command},
		"timeout_ms": req.Timeout,
	})
	reqPtr := sdk.Alloc(int32(len(execReq)))
	sdk.Write(reqPtr, execReq)
	const bufSize = 1 << 20
	resBuf := sdk.Alloc(bufSize)
	n := stadoExec(uint32(reqPtr), uint32(len(execReq)), uint32(resBuf), bufSize)
	if n < 0 {
		return writeError(resPtr, resCap, "exec failed")
	}
	// Pass through the exec result JSON directly.
	result := sdk.Bytes(resBuf, n)
	return writeResult(resPtr, resCap, result)
}

func writeError(resPtr, resCap int32, msg string) int32 {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return writeResult(resPtr, resCap, b)
}

func writeResult(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
```

- [ ] **Step 3: Build + register + parity test**

```
cd internal/bundledplugins/modules/shell
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../../wasm/shell.wasm .
go test ./internal/runtime/... -run TestShellExecParityWasm -v
```

- [ ] **Step 4: Remove native bash, register wasm shell.exec**

```go
reg.Unregister("bash")
reg.Register(newWasmPluginTool("shell", "exec"))
```

- [ ] **Step 5: Commit**

```bash
git add internal/bundledplugins/modules/shell/ internal/bundledplugins/wasm/shell.wasm internal/runtime/executor.go
git commit -m "feat(ep-0038b): shell.exec wasm plugin replaces native bash"
```

---

## Task 4: rg plugin (rg.search via stado_bundled_bin + stado_exec)

Follow the exact same pattern as Task 3, but the wasm module calls `stado_bundled_bin("ripgrep")` to get the path, then builds a `stado_exec` request with that path. Wire name: `rg__search`.

```go
//go:wasmexport stado_tool_rg__search
func stadoToolRGSearch(argsPtr, argsLen, resPtr, resCap int32) int32 {
	// 1. Get ripgrep path via stado_bundled_bin
	nameBuf := []byte("ripgrep")
	namePtr := sdk.Alloc(int32(len(nameBuf)))
	sdk.Write(namePtr, nameBuf)
	pathBuf := sdk.Alloc(512)
	n := stadoBundledBin(uint32(namePtr), uint32(len(nameBuf)), uint32(pathBuf), 512)
	if n < 0 {
		return writeError(resPtr, resCap, "ripgrep binary not available")
	}
	rgPath := string(sdk.Bytes(pathBuf, n))

	// 2. Parse search args
	var req struct {
		Pattern string   `json:"pattern"`
		Paths   []string `json:"paths"`
		Flags   []string `json:"flags"`
	}
	json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req)

	argv := append([]string{rgPath, "--json", req.Pattern}, req.Paths...)
	argv = append(argv, req.Flags...)

	// 3. Run via stado_exec
	execReq, _ := json.Marshal(map[string]any{"argv": argv})
	// ... (same exec pattern as shell)
}
```

Parity test: same input to native `rg.Tool` and wasm `rg__search`.

---

## Task 5: Delete NativeTool wrapper layer (EP-0038 §A, §K)

Only after all tools have wasm replacements and all parity tests pass.

- [ ] **Step 1: Verify all parity tests pass**

```
go test ./internal/runtime/... -run "Parity" -v
```
Expected: all PASS.

- [ ] **Step 2: Delete buildNativeRegistry and wrapper**

In `internal/runtime/bundled_plugin_tools.go`:
- Delete `buildNativeRegistry()`
- Delete `newBundledPluginTool()`
- Delete `buildBundledPluginRegistry()` (replace with direct wasm loading)
- Remove all imports to `internal/tools/bash`, `internal/tools/fs`, etc.

In `internal/runtime/executor.go`, update `BuildDefaultRegistry()`:

```go
func BuildDefaultRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	// Load all bundled wasm plugins.
	for _, entry := range bundledplugins.AllPlugins() {
		reg.Register(newWasmPluginTool(entry.Alias, entry.ToolName))
	}
	registerMetaTools(reg)
	return reg
}
```

- [ ] **Step 3: Run full test suite**

```
go test ./... -count=1 2>&1 | grep -E "FAIL|ok"
```
Expected: no FAILs.

- [ ] **Step 4: Commit**

```bash
git add internal/runtime/bundled_plugin_tools.go internal/runtime/executor.go
git commit -m "feat(ep-0038b): delete NativeTool wrapper — all tools are wasm (EP-0002 invariant restored)"
```

---

## Self-Review

**Spec §C coverage:**

| Plugin | Task |
|---|---|
| `fs` (read/write/edit/glob/grep) | Task 2 |
| `shell` (exec + variants) | Task 3 |
| `rg` (rg.search) | Task 4 |
| `astgrep` | Follow Task 4 pattern |
| `readctx` | Follow Task 2 pattern |
| `lsp` (definition/references/symbols/hover) | New — wraps stado_proc_* for gopls; significant task |
| `web` (fetch/search/browse) | Follow existing webfetch + web-search example |
| `http` (request/client_new) | Follow existing http_request host import |
| `agent` (5 tools) | EP-0038c |
| `mcp` (connect/list_tools/call) | Follow existing mcp-client example plugin |
| `image` (image.info) | Recompile from plugins/examples/image-info |
| `dns` (resolve/reverse) | Follow Task 4 pattern using stado_dns_* |
| `secrets` | Requires stado_secrets_* from EP-0038a |
| `tools` (meta-tools port) | Port native meta_tools.go to wasm |
| `task` | Uses stado_fs_* against state_dir/tasks |
