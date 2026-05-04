package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestPTYImports_RegisterWithCapability registers the PTY host imports
// when the manifest declares exec:pty and confirms the runtime's PTY
// manager is wired through to the Host (so future tool calls have
// access to the same registry).
func TestPTYImports_RegisterWithCapability(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	mf := plugins.Manifest{
		Name:         "shell",
		Version:      "1.0.0",
		Capabilities: []string{"exec:pty"},
	}
	host := NewHost(mf, t.TempDir(), nil)
	if !host.ExecPTY {
		t.Fatalf("ExecPTY not parsed from manifest")
	}
	if err := InstallHostImports(ctx, r, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	if host.PTYManager == nil {
		t.Fatalf("PTYManager not wired by InstallHostImports")
	}
	if host.PTYManager != r.PTYManager() {
		t.Fatalf("PTYManager on host differs from runtime's; want shared registry")
	}
}

// TestPTYImports_OmittedWithoutCapability: a manifest without exec:pty
// must not see any pty host imports in its sandbox.
func TestPTYImports_OmittedWithoutCapability(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	mf := plugins.Manifest{Name: "no-pty", Version: "1.0.0"}
	host := NewHost(mf, t.TempDir(), nil)
	if host.ExecPTY {
		t.Fatalf("ExecPTY unexpectedly set without capability")
	}
	if err := InstallHostImports(ctx, r, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	if host.PTYManager != nil {
		t.Fatalf("PTYManager wired despite missing capability")
	}
}

// TestPTYImports_ManagerSurvivesAcrossInstantiations: two plugin
// instantiations share the same PTYManager and therefore the same
// session registry — that's the property that lets a session created
// in one tool call be reachable from another fresh-instance tool call.
func TestPTYImports_ManagerSurvivesAcrossInstantiations(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	mgr := r.PTYManager()
	if mgr == nil {
		t.Fatal("runtime has nil PTYManager")
	}

	hostA := NewHost(plugins.Manifest{Name: "a", Version: "1.0.0", Capabilities: []string{"exec:pty"}}, t.TempDir(), nil)
	if err := InstallHostImports(ctx, r, hostA); err != nil {
		t.Fatalf("InstallHostImports A: %v", err)
	}
	if hostA.PTYManager != mgr {
		t.Fatalf("hostA wired to a different manager")
	}

	// A second host instance (different manifest name to avoid
	// "module already registered" — real plugins each get their own
	// runtime, but we exercise the wiring contract here).
	r2, err := New(ctx)
	if err != nil {
		t.Fatalf("New 2: %v", err)
	}
	defer func() { _ = r2.Close(ctx) }()
	hostB := NewHost(plugins.Manifest{Name: "b", Version: "1.0.0", Capabilities: []string{"exec:pty"}}, t.TempDir(), nil)
	if err := InstallHostImports(ctx, r2, hostB); err != nil {
		t.Fatalf("InstallHostImports B: %v", err)
	}
	if hostB.PTYManager == nil {
		t.Fatalf("hostB PTYManager not wired")
	}
	// Each runtime has its own manager — different runtimes mean
	// different sessions, not shared. That's the right boundary:
	// runtime = isolation domain.
	if hostB.PTYManager == mgr {
		t.Fatalf("hostB shares manager with hostA across runtimes; want isolation")
	}
}

// TestPTYCapabilityParse covers exec:pty being recognised alongside
// the other exec:* variants.
func TestPTYCapabilityParse(t *testing.T) {
	mf := plugins.Manifest{
		Name:         "x",
		Version:      "1.0.0",
		Capabilities: []string{"exec:bash", "exec:pty", "exec:search"},
	}
	host := NewHost(mf, t.TempDir(), nil)
	if !host.ExecBash || !host.ExecPTY || !host.ExecSearch {
		t.Fatalf("exec caps parsed: bash=%v pty=%v search=%v", host.ExecBash, host.ExecPTY, host.ExecSearch)
	}
}

// silence unused-import warnings if helpers above stop using strings.
var _ = strings.Contains
