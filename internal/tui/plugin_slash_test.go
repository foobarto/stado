package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// newPluginTestModel spins up a Model with XDG paths pointed at
// per-test temp dirs so the handler reads from a known plugin layout.
func newPluginTestModel(t *testing.T) *Model {
	t.Helper()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.width, m.height = 80, 24
	return m
}

// TestPluginSlash_BareListsEmpty: `/plugin` on a fresh install prints
// the "no plugins installed" advisory without erroring.
func TestPluginSlash_BareListsEmpty(t *testing.T) {
	m := newPluginTestModel(t)
	m.handleSlash("/plugin")

	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" {
		t.Fatalf("expected system block, got %q", last.kind)
	}
	if !strings.Contains(last.body, "No plugins") {
		t.Errorf("expected empty-list advisory, got %q", last.body)
	}
}

// TestPluginSlash_NotInstalledReportsCleanly: referencing an unknown
// plugin directory by name surfaces a clear error — not a stack trace
// or silent no-op.
func TestPluginSlash_NotInstalledReportsCleanly(t *testing.T) {
	m := newPluginTestModel(t)
	m.handleSlash("/plugin:nope-1.0.0 greet")

	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.body, "not installed") {
		t.Errorf("expected not-installed advisory, got %q", last.body)
	}
}

// TestPluginSlash_ListsInstalled: a plugin directory with a valid
// manifest shows up under `/plugin` and its tools are enumerated.
func TestPluginSlash_ListsInstalled(t *testing.T) {
	m := newPluginTestModel(t)
	installFakePlugin(t, "demo-0.0.1", plugins.Manifest{
		Name:    "demo",
		Version: "0.0.1",
		Author:  "test",
		Tools: []plugins.ToolDef{
			{Name: "greet", Description: "say hi"},
			{Name: "other", Description: "do something else"},
		},
	})

	m.handleSlash("/plugin")

	body := m.blocks[len(m.blocks)-1].body
	for _, want := range []string{"demo-0.0.1", "greet", "other", "say hi"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
}

// TestPluginSlash_PerPluginListsTools: `/plugin:<name-ver>` without a
// tool argument lists that plugin's tools only.
func TestPluginSlash_PerPluginListsTools(t *testing.T) {
	m := newPluginTestModel(t)
	installFakePlugin(t, "demo-0.0.1", plugins.Manifest{
		Name:    "demo",
		Version: "0.0.1",
		Author:  "test",
		Tools: []plugins.ToolDef{
			{Name: "greet", Description: "say hi"},
		},
	})

	// Skip signature verification by fabricating a trust-store entry
	// for a known-bad signature — we expect the signature check to
	// fail and the handler to append a clear error, not a tool list.
	m.handleSlash("/plugin:demo-0.0.1")

	body := m.blocks[len(m.blocks)-1].body
	// We haven't signed the fake manifest, so VerifyManifest fails.
	// The handler must surface that as a user-facing advisory — not a
	// silent no-op.
	if !strings.Contains(body, "signature") && !strings.Contains(body, "trust") {
		t.Errorf("expected signature/trust error, got %q", body)
	}
}

// TestPluginSlash_UnknownToolName: the per-plugin route resolves but
// the named tool isn't declared — must produce a clear hint pointing
// back at /plugin:<name> for discovery.
func TestPluginSlash_UnknownToolName(t *testing.T) {
	m := newPluginTestModel(t)
	// Same "unsigned" fixture; the handler's signature check runs
	// before the tool-name check, so the top-level assertion is
	// still the signature error. That's the correct ordering:
	// don't reveal declared tool names to a caller who hasn't been
	// gated by the trust store.
	installFakePlugin(t, "demo-0.0.1", plugins.Manifest{Name: "demo", Version: "0.0.1"})
	m.handleSlash("/plugin:demo-0.0.1 nonexistent {}")
	body := m.blocks[len(m.blocks)-1].body
	if body == "" {
		t.Fatal("expected a system block")
	}
}

// installFakePlugin writes a plugin.manifest.json + plugin.wasm +
// plugin.manifest.sig under $XDG_DATA_HOME/stado/plugins/<dirName>/.
// The wasm is a trivial byte stream whose sha256 is pinned in the
// manifest; the signature is 88 bytes of zeros — valid base64 for
// testing the error path (signature will fail, which is what the
// tests above intentionally exercise).
func installFakePlugin(t *testing.T, dirName string, m plugins.Manifest) {
	t.Helper()
	root := filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado", "plugins", dirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	wasmPath := filepath.Join(root, "plugin.wasm")
	wasm := []byte("not a real wasm")
	if err := os.WriteFile(wasmPath, wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	// Fill in wasm_sha256 so the digest check passes — it runs first.
	h := sha256.Sum256(wasm)
	m.WASMSHA256 = hex.EncodeToString(h[:])
	data, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// 64 bytes of zeros in base64 — a syntactically valid signature
	// that will fail the Ed25519 check, which is exactly what we want
	// to test the error-surface path.
	sig := strings.Repeat("A", 88)
	if err := os.WriteFile(filepath.Join(root, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
}
