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
