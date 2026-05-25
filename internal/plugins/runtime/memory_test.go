package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

var singlePageMemoryWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x05, 0x03, 0x01, 0x00, 0x01,
	0x07, 0x0a, 0x01, 0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, 0x02, 0x00,
}

func TestReadBytesRejectsDefaultLimitBeforeBounds(t *testing.T) {
	mod := instantiateMemoryTestModule(t)

	_, err := readBytes(mod.wasmMod, 0, maxPluginRuntimeImportBytes+1)
	if err == nil {
		t.Fatal("readBytes should reject lengths over the default cap")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want cap error", err)
	}
	if strings.Contains(err.Error(), "out-of-bounds") {
		t.Fatalf("limit should be checked before memory bounds: %v", err)
	}
}

func TestReadBytesLimitedRejectsCallSiteLimit(t *testing.T) {
	mod := instantiateMemoryTestModule(t)
	if !mod.wasmMod.Memory().Write(0, []byte("abc")) {
		t.Fatal("failed to seed wasm memory")
	}

	_, err := readBytesLimited(mod.wasmMod, 0, 3, 2)
	if err == nil {
		t.Fatal("readBytesLimited should reject the call-site cap")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want cap error", err)
	}

	got, err := readStringLimited(mod.wasmMod, 0, 3, 3)
	if err != nil {
		t.Fatalf("readStringLimited: %v", err)
	}
	if got != "abc" {
		t.Fatalf("readStringLimited = %q, want abc", got)
	}
}

// TestReadMemoryStringCapped_RejectsOversizeBeforeAlloc covers the
// Codex 2026-05-25 deep-dive cap sweep across host_state.go,
// host_json.go, host_net.go, host_http_stream.go, host_http_upload.go.
// Pre-fix every readMemoryString / Memory().Read call in those files
// trusted the guest-supplied length: a plugin passing 2 GiB would
// force host-side allocation before the bounds check refused the
// read. The Capped variant rejects against a domain-specific cap
// BEFORE touching wasm memory.
func TestReadMemoryStringCapped_RejectsOversizeBeforeAlloc(t *testing.T) {
	mod := instantiateMemoryTestModule(t)
	if !mod.wasmMod.Memory().Write(0, []byte("hello")) {
		t.Fatal("failed to seed wasm memory")
	}

	// Cap = 3, requested length 5 → reject without reading memory.
	got, ok := readMemoryStringCapped(mod.wasmMod, 0, 5, 3)
	if ok {
		t.Fatalf("expected refusal when len > cap; got %q", got)
	}

	// Cap = 5, requested length 5 → accept.
	got, ok = readMemoryStringCapped(mod.wasmMod, 0, 5, 5)
	if !ok {
		t.Fatal("expected acceptance when len == cap")
	}
	if got != "hello" {
		t.Fatalf("got %q, want hello", got)
	}

	// Cap = 5, requested length 3 → accept partial.
	got, ok = readMemoryStringCapped(mod.wasmMod, 0, 3, 5)
	if !ok {
		t.Fatal("expected acceptance when len < cap")
	}
	if got != "hel" {
		t.Fatalf("got %q, want hel", got)
	}
}

// TestReadMemoryBytesCapped_RejectsOversizeBeforeAlloc — symmetrical
// to the string variant. Used by stado_instance_set (value),
// stado_net_write (data), stado_net_sendto (data),
// stado_http_upload_write (data), stado_http_request_stream (args),
// stado_http_upload_create (args).
func TestReadMemoryBytesCapped_RejectsOversizeBeforeAlloc(t *testing.T) {
	mod := instantiateMemoryTestModule(t)
	if !mod.wasmMod.Memory().Write(0, []byte{1, 2, 3, 4, 5}) {
		t.Fatal("failed to seed wasm memory")
	}

	if _, ok := readMemoryBytesCapped(mod.wasmMod, 0, 5, 3); ok {
		t.Fatal("expected refusal when len > cap")
	}

	got, ok := readMemoryBytesCapped(mod.wasmMod, 0, 5, 5)
	if !ok {
		t.Fatal("expected acceptance when len == cap")
	}
	if string(got) != "\x01\x02\x03\x04\x05" {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

// TestReadMemoryStringCapped_OutOfBoundsRejected ensures bounds
// failures still surface even when length <= cap. The two failure
// modes (over-cap, out-of-bounds) collapse into the same return shape
// at the import boundary — both correctly return false.
func TestReadMemoryStringCapped_OutOfBoundsRejected(t *testing.T) {
	mod := instantiateMemoryTestModule(t)
	// Memory is 64 KiB (1 wasm page). Read 16 bytes starting at offset
	// 64K - 8 = 65528. Length is well under any cap but extends 8
	// bytes past the page boundary.
	const pageBytes = 64 * 1024
	if _, ok := readMemoryStringCapped(mod.wasmMod, pageBytes-8, 16, 1024); ok {
		t.Fatal("expected refusal for out-of-bounds read")
	}
}

func instantiateMemoryTestModule(t *testing.T) *Module {
	t.Helper()
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	mod, err := rt.Instantiate(ctx, singlePageMemoryWasm, plugins.Manifest{
		Name:    "memory-test",
		Version: t.Name(),
	})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	return mod
}
