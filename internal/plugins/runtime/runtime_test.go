package runtime

import (
	"context"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// minimalWasm is a 2-module WebAssembly binary — no code, just a module
// header + version (MVP 1). Instantiate must succeed on this; it's the
// smallest valid input.
//
//	00 61 73 6d  01 00 00 00  = \0asm + version 1
var minimalWasm = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

func TestNewAndClose(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Double-close is a no-op.
	if err := r.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestInstantiateMinimalModule(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	mod, err := r.Instantiate(ctx, minimalWasm, plugins.Manifest{
		Name:    "noop",
		Version: "0.1.0",
	})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if mod.Name != "noop" || mod.Version != "0.1.0" {
		t.Errorf("module metadata: %+v", mod)
	}
	if err := mod.Close(ctx); err != nil {
		t.Fatalf("Module.Close: %v", err)
	}
}

func TestInstantiateAfterCloseRejects(t *testing.T) {
	ctx := context.Background()
	r, _ := New(ctx)
	_ = r.Close(ctx)
	_, err := r.Instantiate(ctx, minimalWasm, plugins.Manifest{Name: "x", Version: "0"})
	if err == nil {
		t.Fatal("expected error instantiating under a closed runtime")
	}
}

func TestInstantiateRejectsGarbageBytes(t *testing.T) {
	ctx := context.Background()
	r, _ := New(ctx)
	defer func() { _ = r.Close(ctx) }()

	_, err := r.Instantiate(ctx, []byte{0xde, 0xad, 0xbe, 0xef}, plugins.Manifest{
		Name: "bad", Version: "0.0.1",
	})
	if err == nil {
		t.Fatal("expected error on non-wasm input")
	}
}

func TestImportErrorString(t *testing.T) {
	e := &ImportError{Func: "stado_fs_write", Reason: "path not in manifest"}
	got := e.Error()
	want := "wazero host: stado_fs_write denied: path not in manifest"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
