package runtime

import "testing"

// TestLookupToolMetadata_ResolutionOrder locks the new metadata
// pipeline: hidden → canonical literal → wire-form parse → legacy
// bare alias → unknown bare. Pre-2026-05-09 the metadata table held
// duplicate entries for bare and wire forms; the dedup left a single
// source of truth keyed on canonical, with three fall-through paths
// for non-canonical inputs. Lock all three in.
func TestLookupToolMetadata_ResolutionOrder(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantCanonical string
		wantPlugin    string
	}{
		// 1. Canonical literal hits the canonical map directly.
		{"canonical fs.read", "fs.read", "fs.read", "fs"},
		{"canonical shell.snapshot", "shell.snapshot", "shell.snapshot", "shell"},
		{"canonical browser.cdp_eval", "browser.cdp_eval", "browser.cdp_eval", "browser"},

		// 2. Wire form parses and reaches the canonical map.
		{"wire fs__read", "fs__read", "fs.read", "fs"},
		{"wire shell__snapshot", "shell__snapshot", "shell.snapshot", "shell"},
		{"wire agent__send_message", "agent__send_message", "agent.send_message", "agent"},

		// 3. Legacy bare alias → canonical (the pre-EP-0038 surface).
		{"legacy read → fs.read", "read", "fs.read", "fs"},
		{"legacy bash → shell.exec", "bash", "shell.exec", "shell"},
		{"legacy ripgrep → rg.search", "ripgrep", "rg.search", "rg"},
		{"legacy find_definition → lsp.definition", "find_definition", "lsp.definition", "lsp"},

		// 4. Unknown wire form: synthesise from the alias prefix.
		{"unknown wire form", "myplugin__customtool", "myplugin.customtool", "myplugin"},

		// 5. Unknown bare name: return literal, no plugin.
		{"unknown bare", "unrecognised", "unrecognised", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			md := LookupToolMetadata(c.input)
			if md.Canonical != c.wantCanonical {
				t.Errorf("LookupToolMetadata(%q).Canonical = %q, want %q", c.input, md.Canonical, c.wantCanonical)
			}
			if md.Plugin != c.wantPlugin {
				t.Errorf("LookupToolMetadata(%q).Plugin = %q, want %q", c.input, md.Plugin, c.wantPlugin)
			}
		})
	}
}

// TestLookupToolMetadata_HiddenSuppressed confirms the legacy hidden
// tools (`ls`, `webfetch`) return a zero ToolMetadata so the listing
// code can suppress them. Without this, the operator-facing tool
// list would show both the legacy bare name and its wasm replacement.
func TestLookupToolMetadata_HiddenSuppressed(t *testing.T) {
	for _, name := range []string{"ls", "webfetch"} {
		md := LookupToolMetadata(name)
		if md.Canonical != "" {
			t.Errorf("LookupToolMetadata(%q): hidden tools should return empty Canonical; got %+v", name, md)
		}
	}
}
