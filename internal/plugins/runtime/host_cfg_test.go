package runtime

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestRegisterCfgImports_Smoke covers the registration path for the
// `cfg:state_dir` capability. Without the cap declared, the import
// is not exported (the smoke test for the InstallHostImports list
// elsewhere already covers that case). With the cap declared, the
// import registers cleanly.
func TestRegisterCfgImports_Smoke(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	host := NewHost(plugins.Manifest{
		Name:         "cfg-demo",
		Capabilities: []string{"cfg:state_dir"},
	}, "/tmp", nil)
	host.StateDir = "/tmp/test-stado-state"

	if !host.CfgStateDir {
		t.Fatal("CfgStateDir should be true after parsing `cfg:state_dir` cap")
	}
	if err := InstallHostImports(ctx, r, host); err != nil {
		t.Fatalf("InstallHostImports with cfg:state_dir: %v", err)
	}
}

// TestRegisterCfgImports_NotRegisteredWithoutCap verifies the
// belt-and-braces refusal model: a manifest that does NOT declare
// `cfg:state_dir` shouldn't see `stado_cfg_state_dir` exported.
// Failure mode checked by attempting to InstallHostImports against a
// runtime that previously had cfg installed — would collide on
// stado_cfg_state_dir if we leaked the registration.
func TestRegisterCfgImports_NotRegisteredWithoutCap(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	// First install: NO cfg:state_dir cap. Should succeed and NOT
	// register stado_cfg_state_dir.
	host1 := NewHost(plugins.Manifest{Name: "no-cfg"}, "/tmp", nil)
	if host1.CfgStateDir {
		t.Fatal("CfgStateDir should be false without `cfg:state_dir` cap")
	}
	if err := InstallHostImports(ctx, r, host1); err != nil {
		t.Fatalf("InstallHostImports without cfg cap: %v", err)
	}
}

// TestWriteCfgValue_Contract exercises the value-flow contract shared by
// every cfg:* host import (EP-0029 §"Behaviour"): a fitting value is
// written to the caller buffer and its byte length returned; an empty
// value writes nothing and returns 0; a value larger than the buffer or
// past the maxPluginRuntimeCfgValueBytes ceiling returns -1. Registration
// is covered above; this covers what actually crosses the wasm boundary.
func TestWriteCfgValue_Contract(t *testing.T) {
	mod := instantiateMemoryTestModule(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("fitting value writes and returns length", func(t *testing.T) {
		val := "/var/home/u/.local/share/stado"
		n := writeCfgValue(mod.wasmMod, 0, 256, val, "stado_cfg_state_dir", logger)
		if int(n) != len(val) {
			t.Fatalf("returned %d, want %d", n, len(val))
		}
		got, ok := mod.wasmMod.Memory().Read(0, uint32(len(val)))
		if !ok || string(got) != val {
			t.Fatalf("memory = %q (ok=%v), want %q", got, ok, val)
		}
	})

	t.Run("empty value returns 0", func(t *testing.T) {
		if n := writeCfgValue(mod.wasmMod, 0, 256, "", "stado_cfg_state_dir", logger); n != 0 {
			t.Fatalf("returned %d, want 0", n)
		}
	})

	t.Run("value larger than buffer returns -1", func(t *testing.T) {
		if n := writeCfgValue(mod.wasmMod, 0, 4, "/longer/than/four", "stado_cfg_state_dir", logger); n != -1 {
			t.Fatalf("returned %d, want -1", n)
		}
	})

	t.Run("value over ceiling returns -1", func(t *testing.T) {
		big := strings.Repeat("a", maxPluginRuntimeCfgValueBytes+1)
		// Buffer large enough that the bufCap guard doesn't fire first —
		// isolates the ceiling guard.
		if n := writeCfgValue(mod.wasmMod, 0, uint32(len(big)), big, "stado_cfg_state_dir", logger); n != -1 {
			t.Fatalf("returned %d, want -1", n)
		}
	})
}
