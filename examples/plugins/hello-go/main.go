// hello-go — Go version of the stado plugin example. Mirrors the
// semantics of examples/plugins/hello (Zig) so you can see the same
// tool implemented in both languages against the same ABI.
//
// Build target: wasip1 + `-buildmode=c-shared`, which produces a WASI
// reactor module. `//go:wasmexport` marks our ABI entry points;
// `//go:wasmimport stado ...` wires up host imports.
//
// Caveat: Go's wasm output includes the full Go runtime (goroutine
// scheduler, GC, memory allocator). This makes the binary ~1.5 MB —
// two orders of magnitude larger than the Zig sibling (~800 bytes).
// For plugins that need rich Go stdlib or existing packages, the size
// is worth paying; for tight tools, Zig / Rust / TinyGo are leaner.
package main

import (
	"encoding/json"
	"sync"
	"unsafe"
)

// main is required for `buildmode=c-shared` but never runs as a reactor
// — the host instantiates the module and calls exports directly.
func main() {}

// ---- Host imports (module "stado") -------------------------------------

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

func logInfo(msg string) {
	const level = "info"
	levelBytes := []byte(level)
	msgBytes := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&levelBytes[0]))), uint32(len(levelBytes)),
		uint32(uintptr(unsafe.Pointer(&msgBytes[0]))), uint32(len(msgBytes)),
	)
}

// ---- Allocator plumbing ------------------------------------------------
//
// stado_alloc returns a wasm linear-memory offset; stado_free releases
// it. We back allocations with Go slices and pin them in a sync.Map so
// the GC doesn't collect a buffer while the host still has the pointer.

var pinned sync.Map // key: uintptr → value: []byte (keeps the backing store alive)

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) {
	pinned.Delete(uintptr(ptr))
	_ = size
}

// ---- Tool: greet -------------------------------------------------------
//
// ABI:
//   stado_tool_greet(argsPtr, argsLen, resultPtr, resultCap) → i32
// Reads JSON {"name":"..."} at argsPtr, writes {"message":"Hello, <name>!"}
// at resultPtr, returns bytes written (or -1 on error).

type greetArgs struct {
	Name string `json:"name"`
}

type greetResult struct {
	Message string `json:"message"`
}

//go:wasmexport stado_tool_greet
func stadoToolGreet(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("greet invoked")

	// Zero-copy view into the host-provided args buffer.
	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))

	// Parse. Missing / empty name → default "world".
	var a greetArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return -1
		}
	}
	name := a.Name
	if name == "" {
		name = "world"
	}

	payload, err := json.Marshal(greetResult{Message: "Hello, " + name + "!"})
	if err != nil {
		return -1
	}
	if int32(len(payload)) > resultCap {
		return -1
	}

	// Write directly into the host-provided result buffer.
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, payload)
	return int32(len(payload))
}
