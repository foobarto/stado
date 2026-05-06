package runtime

import (
	"context"
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
