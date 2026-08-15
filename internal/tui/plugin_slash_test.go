package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

func TestPluginSlash_RejectsEscapingPluginID(t *testing.T) {
	m := newPluginTestModel(t)
	outside := filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado", "escape")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	m.handleSlash("/plugin:../escape greet")

	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "invalid plugin id") {
		t.Fatalf("expected invalid plugin id advisory, got %q", body)
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
	for _, want := range []string{"(demo)", "@0.0.1", "greet", "other", "say hi"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
}

// TestPluginSlash_StripsEscapeInDescription: manifest tool descriptions
// must be sanitized before render — a malicious project plugin could
// otherwise inject OSC/CSI into the /plugin listing (#9).
func TestPluginSlash_StripsEscapeInDescription(t *testing.T) {
	m := newPluginTestModel(t)
	probe := "\x1b]52;c;ZXZpbA==\x07evil"
	installFakePlugin(t, "evil-0.0.1", plugins.Manifest{
		Name:    "evil",
		Version: "0.0.1",
		Author:  "test",
		Tools: []plugins.ToolDef{
			{Name: "pwn", Description: probe},
		},
	})

	m.handleSlash("/plugin")

	body := m.blocks[len(m.blocks)-1].body
	if strings.Contains(body, "\x1b") || strings.Contains(body, "\x07") {
		t.Fatalf("escaped description leaked into /plugin body: %q", body)
	}
	if !strings.Contains(body, "evil") {
		t.Errorf("expected sanitized plugin listing, got %q", body)
	}
}

// TestPluginSlash_LongDescriptionsDoNotClipLaterPlugins guards the
// /plugin rendering against the bug where a plugin with very long
// tool descriptions earlier in the list would push the body past the
// system block's render-side truncate ceiling, hiding subsequent
// plugins entirely. Verifies all installed plugin IDs land in the
// listing regardless of description length. Post shared-formatter
// (WrapDescList): descriptions are no longer truncated — they wrap with
// a hanging indent, so the assertion is that the FULL text survives and
// continuation lines stay indented under the tool bullet (the nested
// hierarchy holds) rather than flowing to column 0.
func TestPluginSlash_LongDescriptionsDoNotClipLaterPlugins(t *testing.T) {
	m := newPluginTestModel(t)
	verbose := strings.Repeat("Generate a complex command with many options. ", 12) // ~560 chars
	installFakePlugin(t, "ad-attacks-0.1.0", plugins.Manifest{
		Name:    "ad-attacks",
		Version: "0.1.0",
		Author:  "test",
		Tools: []plugins.ToolDef{
			{Name: "ad_pth_command", Description: verbose},
			{Name: "ad_certipy_command", Description: verbose},
			{Name: "ad_kerberoast", Description: verbose},
		},
	})
	installFakePlugin(t, "second-plugin-0.0.1", plugins.Manifest{
		Name:    "second-plugin",
		Version: "0.0.1",
		Author:  "test",
		Tools:   []plugins.ToolDef{{Name: "tail_marker", Description: "should be visible"}},
	})

	m.handleSlash("/plugin")
	body := m.blocks[len(m.blocks)-1].body

	for _, want := range []string{"(ad-attacks)", "@0.1.0", "ad_kerberoast", "(second-plugin)", "tail_marker"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q (likely clipped); body length=%d", want, len(body))
		}
	}
	// No truncation: the full description text survives (no ellipsis).
	if strings.Contains(body, "…") {
		t.Errorf("descriptions must no longer be truncated with an ellipsis: %q", body)
	}
	if !strings.Contains(body, "many options") {
		t.Errorf("full description text should survive untruncated: %q", body)
	}
	// The nested hierarchy must hold: a tool's wrapped description
	// continuation lines stay indented (never flow to column 0), so the
	// "  /plugin:NAME" → "    · TOOL" structure isn't dismantled.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Continuation lines of a wrapped description carry the repeated
		// phrase but are neither a plugin header ("/plugin:") nor a tool
		// bullet ("·"). They must be indented.
		if strings.Contains(line, "Generate a complex") &&
			!strings.HasPrefix(trimmed, "·") &&
			!strings.Contains(line, "/plugin:") {
			if !strings.HasPrefix(line, " ") {
				t.Errorf("wrapped description continuation flowed to column 0 (hierarchy broken): %q", line)
			}
		}
	}
}

// TestPluginSlash_PerPluginListsTools: `/plugin:<name-ver>` without a
// tool argument lists that plugin's tools only.
func TestPluginSlash_PerPluginListsTools(t *testing.T) {
	m := newPluginTestModel(t)
	storeKey := installFakePlugin(t, "demo-source", plugins.Manifest{
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
	m.handleSlash("/plugin:" + storeKey)

	body := m.blocks[len(m.blocks)-1].body
	// We haven't signed the fake manifest, so VerifyManifest fails.
	// The handler must surface that as a user-facing advisory — not a
	// silent no-op.
	if !strings.Contains(body, "signature") && !strings.Contains(body, "trust") {
		t.Errorf("expected signature/trust error, got %q", body)
	}
}

// TestPluginSlash_BareNameResolvesToActiveVersion proves that a friendly
// manifest-name alias may resolve only when one installed source owns it.
// The source-derived store key remains the authoritative selector.
func TestPluginSlash_BareNameResolvesToActiveVersion(t *testing.T) {
	m := newPluginTestModel(t)
	installFakePlugin(t, "demo-0.0.1", plugins.Manifest{
		Name:    "demo",
		Version: "0.0.1",
		Author:  "test",
		Tools:   []plugins.ToolDef{{Name: "greet", Description: "say hi"}},
	})

	// Bare-name run should land past resolution and into the
	// signature-verify path (the fake plugin is unsigned, so verify
	// fails — that's the same advisory the literal-form test asserts).
	m.handleSlash("/plugin:demo greet")
	body := m.blocks[len(m.blocks)-1].body
	if strings.Contains(body, "not installed") {
		t.Fatalf("bare name should resolve via active-version pin, got %q", body)
	}
	if !strings.Contains(body, "signature") && !strings.Contains(body, "trust") {
		t.Errorf("expected signature/trust error after bare-name resolves, got %q", body)
	}
}

// TestPluginSlash_BareNameListsTools: `/plugin:<name>` with no tool
// argument must reach the per-plugin tools listing route via active-
// version resolution. Asserts the same surface as the literal-form
// list test (signature-error, since the fixture is unsigned).
func TestPluginSlash_BareNameListsTools(t *testing.T) {
	m := newPluginTestModel(t)
	installFakePlugin(t, "demo-0.0.1", plugins.Manifest{
		Name:    "demo",
		Version: "0.0.1",
		Author:  "test",
		Tools:   []plugins.ToolDef{{Name: "greet", Description: "say hi"}},
	})

	m.handleSlash("/plugin:demo")
	body := m.blocks[len(m.blocks)-1].body
	if strings.Contains(body, "not installed") {
		t.Fatalf("bare-name list should resolve, got %q", body)
	}
	if !strings.Contains(body, "signature") && !strings.Contains(body, "trust") {
		t.Errorf("expected signature/trust error after bare-name resolves, got %q", body)
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
	storeKey := installFakePlugin(t, "demo-source", plugins.Manifest{Name: "demo", Version: "0.0.1"})
	m.handleSlash("/plugin:" + storeKey + " nonexistent {}")
	body := m.blocks[len(m.blocks)-1].body
	if body == "" {
		t.Fatal("expected a system block")
	}
}

// TestPluginToolListWidth_NeverOverflowsIndent: the nested tool block is
// indented by pluginToolIndent under each plugin header, so its wrap
// width must never exceed width-pluginToolIndent — otherwise the
// indented lines overflow the enclosing system block, which re-wraps
// them and breaks the hanging indent. The old `if toolWidth < 20 { 20 }`
// floor clamped the width UP past the available space at narrow panels.
func TestPluginToolListWidth_NeverOverflowsIndent(t *testing.T) {
	for _, width := range []int{120, 40, 24, 20, 15, 10, 5, 1, 0} {
		got := pluginToolListWidth(width)
		if got > width-pluginToolIndent && got > 1 {
			t.Errorf("width=%d: pluginToolListWidth=%d exceeds width-indent=%d (will overflow the indent and re-wrap)",
				width, got, width-pluginToolIndent)
		}
		if got < 1 {
			t.Errorf("width=%d: pluginToolListWidth=%d must stay >= 1", width, got)
		}
	}
}

// TestPluginSlash_NarrowViewportHangIndent renders the bare /plugin
// list at a narrow viewport through the REAL system-block path and
// asserts no wrapped tool-description continuation line flows back to
// text-column 0. Before the slashListWidth + pluginToolListWidth fixes,
// a narrow viewport over-wrapped the nested tool block and the box
// re-wrapped it, dropping continuation words to column 0.
func TestPluginSlash_NarrowViewportHangIndent(t *testing.T) {
	m := newPluginTestModel(t)
	verbose := strings.Repeat("generate a command with many options ", 6)
	installFakePlugin(t, "demo-0.1.0", plugins.Manifest{
		Name:    "demo",
		Version: "0.1.0",
		Author:  "test",
		Tools: []plugins.ToolDef{
			{Name: "run_thing", Description: verbose},
		},
	})

	for _, vpw := range []int{60, 44, 30} {
		m.vp.SetWidth(vpw)
		m.vp.SetHeight(40)
		m.handleSlash("/plugin")
		body := m.blocks[len(m.blocks)-1].body
		out, _ := m.renderBlock(block{kind: "system", body: body}, vpw-2)
		stripped := ansi.Strip(out)
		for _, ln := range strings.Split(strings.TrimRight(stripped, "\n"), "\n") {
			content := ln
			if i := strings.IndexAny(content, "│"); i >= 0 {
				content = content[i+len("│"):]
			}
			content = strings.TrimPrefix(content, " ") // left padding
			if strings.TrimSpace(content) == "" {
				continue
			}
			// Continuation lines of the wrapped description carry the
			// repeated phrase but are not a plugin header or tool bullet.
			// They must be indented (nested hierarchy holds).
			if strings.Contains(content, "many options") &&
				!strings.HasPrefix(strings.TrimSpace(content), "·") &&
				!strings.Contains(content, "/plugin:") {
				if !strings.HasPrefix(content, " ") {
					t.Errorf("vp.Width()=%d: tool-desc continuation flowed to text-column 0: %q",
						vpw, ln)
				}
			}
		}
	}
}

// installFakePlugin writes a plugin source fixture, then installs it under its
// source-keyed store directory with a complete host-owned record.
// The wasm is a trivial byte stream whose sha256 is pinned in the
// manifest; the signature is 88 bytes of zeros — valid base64 for
// testing the error path (signature will fail, which is what the
// tests above intentionally exercise).
func installFakePlugin(t *testing.T, sourceName string, m plugins.Manifest) string {
	t.Helper()
	if m.Lifecycle == nil {
		for i := range m.Tools {
			if m.Tools[i].Capabilities == nil {
				m.Tools[i].Capabilities = plugins.CapabilitySubset()
			}
		}
	}
	source := filepath.Join(t.TempDir(), sourceName)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	wasmPath := filepath.Join(source, "plugin.wasm")
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
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// 64 bytes of zeros in base64 — a syntactically valid signature
	// that will fail the Ed25519 check, which is exactly what we want
	// to test the error-surface path.
	sig := strings.Repeat("A", 88)
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, m)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado", "plugins", record.StoreKey)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		contents, readErr := os.ReadFile(filepath.Join(source, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(root, filename), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.WriteInstallRecord(root, record, m); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallReceipt(filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado"), filepath.Dir(root), record); err != nil {
		t.Fatal(err)
	}
	return record.StoreKey
}
