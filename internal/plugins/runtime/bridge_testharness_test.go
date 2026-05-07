package runtime

import (
	"context"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// bridgeHarness wires a Runtime + Host with chosen bridges and
// capabilities, then exposes helpers to invoke stado_* host imports
// through a thunk wasm module so the contract tests in Phase 1.1 of
// the 2026-Q2 refactor program can drive the real production import
// closures.
//
// The contracts every bridge satisfies (per the plan):
//
//  1. Capability gate. Cap denied → host import returns -1 without
//     reaching the bridge.
//  2. Nil-bridge. Bridge field nil → host import returns the
//     "unavailable" sentinel without panicking.
//  3. Exact forwarding. Cap + bridge present → arguments forward
//     unchanged to the bridge; bridge result returns to the import
//     unchanged.
//  4. Cancel propagation. ctx cancellation reaches the bridge call
//     where the bridge accepts ctx; the import returns the
//     cancellation-mapped sentinel.
//
// Wazero forbids ExportedFunction on host modules, so the harness
// builds a separate thunk wasm module that imports each stado_*
// function and exports a same-shape thunk per import. The thunk
// has its own linear memory exported as "memory" so tests can
// stage input payloads before invocation and read back outputs.
type bridgeHarness struct {
	t        *testing.T
	rt       *Runtime
	host     *Host
	thunkMod api.Module // populated by install()
	thunks   []thunkImport
}

// thunkImport names one stado_* host import we want to drive from
// tests. NumParams / HasResult must match the import's actual wasm
// signature; otherwise InstantiateWithConfig fails at link time.
type thunkImport struct {
	Field     string // e.g. "stado_session_read"
	NumParams int    // count of i32 params; 0..N
	HasResult bool   // true → returns i32
}

// allBridgeImports lists every host import touched by the four
// bridges in scope for Phase 1.1. Tests instantiate the harness
// with this set so any bridge call dispatches through a real
// thunk → import → closure path.
var allBridgeImports = []thunkImport{
	// SessionBridge
	{"stado_session_read", 4, true},
	{"stado_session_next_event", 2, true},
	{"stado_session_fork", 6, true},
	{"stado_llm_invoke", 4, true},
	// MemoryBridge
	{"stado_memory_propose", 2, true},
	{"stado_memory_query", 4, true},
	{"stado_memory_update", 2, true},
	// ApprovalBridge
	{"stado_ui_approve", 4, true},
	// ChoiceBridge
	{"stado_ui_choose", 4, true},
	// FleetBridge
	{"stado_agent_spawn", 4, true},
	{"stado_agent_list", 2, true},
	{"stado_agent_read_messages", 4, true},
	{"stado_agent_send_message", 2, true},
	{"stado_agent_cancel", 4, true},
}

// newBridgeHarness builds a harness with no capabilities declared
// and no bridges wired (default-deny). Tests opt in via the
// withCaps / with*Bridge methods before calling install().
func newBridgeHarness(t *testing.T) *bridgeHarness {
	t.Helper()
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	host := NewHost(plugins.Manifest{Name: "test"}, "/tmp", nil)
	return &bridgeHarness{t: t, rt: rt, host: host, thunks: allBridgeImports}
}

// withCaps re-parses the manifest with the given capability strings
// appended. Must be called BEFORE install().
func (h *bridgeHarness) withCaps(caps ...string) *bridgeHarness {
	h.t.Helper()
	if h.thunkMod != nil {
		h.t.Fatal("withCaps must be called before install()")
	}
	m := h.host.Manifest
	m.Capabilities = append(append([]string{}, m.Capabilities...), caps...)
	newH := NewHost(m, h.host.Workdir, h.host.Logger)
	// Carry forward any bridges already wired.
	newH.SessionBridge = h.host.SessionBridge
	newH.MemoryBridge = h.host.MemoryBridge
	newH.ApprovalBridge = h.host.ApprovalBridge
	newH.ChoiceBridge = h.host.ChoiceBridge
	newH.FleetBridge = h.host.FleetBridge
	h.host = newH
	return h
}

func (h *bridgeHarness) withSessionBridge(b SessionBridge) *bridgeHarness {
	h.host.SessionBridge = b
	return h
}
func (h *bridgeHarness) withMemoryBridge(b MemoryBridge) *bridgeHarness {
	h.host.MemoryBridge = b
	return h
}
func (h *bridgeHarness) withApprovalBridge(b ApprovalBridge) *bridgeHarness {
	h.host.ApprovalBridge = b
	return h
}
func (h *bridgeHarness) withChoiceBridge(b ChoiceBridge) *bridgeHarness {
	h.host.ChoiceBridge = b
	return h
}
func (h *bridgeHarness) withFleetBridge(b FleetBridge) *bridgeHarness {
	h.host.FleetBridge = b
	return h
}

// install registers stado host imports against the configured Host
// and instantiates the thunk wasm module so callImport can drive
// the imports through a real wasm caller.
func (h *bridgeHarness) install() *bridgeHarness {
	h.t.Helper()
	ctx := context.Background()
	if err := InstallHostImports(ctx, h.rt, h.host); err != nil {
		h.t.Fatalf("InstallHostImports: %v", err)
	}
	wasmBytes := encodeThunkModule(NamespaceStado, h.thunks)
	cfg := wazero.NewModuleConfig().WithName("test_thunks")
	mod, err := h.rt.Wazero().InstantiateWithConfig(ctx, wasmBytes, cfg)
	if err != nil {
		h.t.Fatalf("instantiate thunk module: %v", err)
	}
	h.t.Cleanup(func() { _ = mod.Close(ctx) })
	h.thunkMod = mod
	return h
}

// callImport invokes the named host import via its thunk in the
// test wasm module. Args are the same i32 stack values the wasm
// plugin would pass. Returns the i32 the import wrote.
func (h *bridgeHarness) callImport(ctx context.Context, name string, args ...uint64) int32 {
	h.t.Helper()
	if h.thunkMod == nil {
		h.t.Fatal("install() must be called before callImport()")
	}
	fn := h.thunkMod.ExportedFunction("thunk_" + name)
	if fn == nil {
		h.t.Fatalf("thunk_%s not exported by test thunk module", name)
	}
	results, err := fn.Call(ctx, args...)
	if err != nil {
		h.t.Fatalf("call thunk_%s: %v", name, err)
	}
	if len(results) != 1 {
		h.t.Fatalf("thunk_%s: expected 1 result, got %d", name, len(results))
	}
	return api.DecodeI32(results[0])
}

// memWrite stages bytes into the thunk module's exported memory at
// the given offset. Used to seed input payloads before calling an
// import that reads from wasm memory.
func (h *bridgeHarness) memWrite(offset uint32, data []byte) {
	h.t.Helper()
	if !h.thunkMod.Memory().Write(offset, data) {
		h.t.Fatalf("memWrite(%d, len=%d): out of bounds", offset, len(data))
	}
}

// memRead returns a copy of `length` bytes starting at `offset` in
// the thunk module's exported memory. Used to read back results an
// import wrote.
func (h *bridgeHarness) memRead(offset, length uint32) []byte {
	h.t.Helper()
	out, ok := h.thunkMod.Memory().Read(offset, length)
	if !ok {
		h.t.Fatalf("memRead(%d, %d): out of bounds", offset, length)
	}
	return append([]byte(nil), out...)
}

// ---- wasm encoder --------------------------------------------------------
//
// Minimal binary encoder for the test thunk module. Emits:
//   - one type per unique signature among `imports`
//   - one import entry per `imports` (all from `module` namespace)
//   - one defined function per import with the same signature; body
//     is `local.get 0..N-1; call $import; end`
//   - one memory of 1 page, exported as "memory"
//   - one export "thunk_<field>" per import → defined function index
//
// Format reference: https://webassembly.github.io/spec/core/binary/

func encodeThunkModule(module string, imports []thunkImport) []byte {
	var w wasmWriter

	// Magic + version.
	w.bytes(0x00, 0x61, 0x73, 0x6d) // "\0asm"
	w.bytes(0x01, 0x00, 0x00, 0x00) // version 1

	// Compute unique signatures.
	sigKey := func(t thunkImport) uint64 {
		// Pack (numParams, hasResult) into one key.
		var r uint64
		if t.HasResult {
			r = 1
		}
		return uint64(t.NumParams)<<1 | r
	}
	sigOrder := []uint64{}
	sigSpec := map[uint64]thunkImport{}
	sigIdx := map[uint64]uint32{}
	for _, t := range imports {
		k := sigKey(t)
		if _, ok := sigIdx[k]; ok {
			continue
		}
		sigIdx[k] = uint32(len(sigOrder))
		sigOrder = append(sigOrder, k)
		sigSpec[k] = t
	}

	// Section 1: types.
	{
		var s wasmWriter
		s.uleb128(uint32(len(sigOrder)))
		for _, k := range sigOrder {
			t := sigSpec[k]
			s.bytes(0x60) // functype
			s.uleb128(uint32(t.NumParams))
			for i := 0; i < t.NumParams; i++ {
				s.bytes(0x7f) // i32
			}
			if t.HasResult {
				s.uleb128(1)
				s.bytes(0x7f) // i32
			} else {
				s.uleb128(0)
			}
		}
		w.section(1, s.buf)
	}

	// Section 2: imports.
	{
		var s wasmWriter
		s.uleb128(uint32(len(imports)))
		for _, t := range imports {
			s.name(module)
			s.name(t.Field)
			s.bytes(0x00) // import desc: function
			s.uleb128(sigIdx[sigKey(t)])
		}
		w.section(2, s.buf)
	}

	// Section 3: function (one defined per import).
	{
		var s wasmWriter
		s.uleb128(uint32(len(imports)))
		for _, t := range imports {
			s.uleb128(sigIdx[sigKey(t)])
		}
		w.section(3, s.buf)
	}

	// Section 5: memory (one, min=1 page, no max).
	{
		var s wasmWriter
		s.uleb128(1)  // 1 memory
		s.bytes(0x00) // limits flags: no max
		s.uleb128(1)  // initial pages
		w.section(5, s.buf)
	}

	// Section 7: exports — memory + each thunk.
	{
		var s wasmWriter
		s.uleb128(uint32(len(imports) + 1))
		// memory export
		s.name("memory")
		s.bytes(0x02) // export desc: memory
		s.uleb128(0)
		// each thunk: defined function index = len(imports) + i
		for i, t := range imports {
			s.name("thunk_" + t.Field)
			s.bytes(0x00) // export desc: function
			s.uleb128(uint32(len(imports)) + uint32(i))
		}
		w.section(7, s.buf)
	}

	// Section 10: code.
	{
		var s wasmWriter
		s.uleb128(uint32(len(imports)))
		for i, t := range imports {
			var body wasmWriter
			body.uleb128(0) // 0 local groups
			for p := 0; p < t.NumParams; p++ {
				body.bytes(0x20) // local.get
				body.uleb128(uint32(p))
			}
			body.bytes(0x10) // call
			body.uleb128(uint32(i))
			body.bytes(0x0b) // end
			s.uleb128(uint32(len(body.buf)))
			s.buf = append(s.buf, body.buf...)
		}
		w.section(10, s.buf)
	}

	return w.buf
}

type wasmWriter struct {
	buf []byte
}

func (w *wasmWriter) bytes(b ...byte) {
	w.buf = append(w.buf, b...)
}

// uleb128 writes an unsigned LEB128.
func (w *wasmWriter) uleb128(v uint32) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			w.buf = append(w.buf, b)
			return
		}
		w.buf = append(w.buf, b|0x80)
	}
}

// name writes a wasm name: ULEB128 length + UTF-8 bytes.
func (w *wasmWriter) name(s string) {
	w.uleb128(uint32(len(s)))
	w.buf = append(w.buf, s...)
}

// section writes a section: id (1 byte) + size (ULEB128) + content.
func (w *wasmWriter) section(id byte, content []byte) {
	w.buf = append(w.buf, id)
	w.uleb128(uint32(len(content)))
	w.buf = append(w.buf, content...)
}

// TestBridgeHarness_Probe smoke-tests the harness end-to-end:
// builds the thunk module, instantiates it against the runtime
// (which links against the stado host imports), invokes
// stado_session_read with no caps + no bridge wired, and asserts
// the closure returns -1.
func TestBridgeHarness_Probe(t *testing.T) {
	h := newBridgeHarness(t).install()
	got := h.callImport(context.Background(), "stado_session_read", 0, 0, 0, 0)
	if got != -1 {
		t.Fatalf("expected -1 (cap denied), got %d", got)
	}
}
