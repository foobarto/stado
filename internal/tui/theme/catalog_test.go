package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogLoadsBundledThemes(t *testing.T) {
	entries := Catalog()
	if len(entries) < 4 {
		t.Fatalf("catalog size = %d, want at least 4", len(entries))
	}
	seenLight := false
	seenRose := false
	for _, entry := range entries {
		th, err := Named(entry.ID)
		if err != nil {
			t.Fatalf("Named(%q): %v", entry.ID, err)
		}
		if th.Name != entry.ID {
			t.Fatalf("theme name = %q, want %q", th.Name, entry.ID)
		}
		if entry.Mode == "light" {
			seenLight = true
		}
		if entry.ID == "stado-rose" && th.Colors.Primary == "#f18fb3" {
			seenRose = true
		}
	}
	if !seenLight {
		t.Fatal("catalog should include a light theme")
	}
	if !seenRose {
		t.Fatal("catalog should include the rose theme")
	}
}

func TestBuiltinTOMLReturnsCopy(t *testing.T) {
	a, ok := BuiltinTOML("stado-dark")
	if !ok {
		t.Fatal("missing stado-dark")
	}
	b, ok := BuiltinTOML("stado-dark")
	if !ok {
		t.Fatal("missing stado-dark on second lookup")
	}
	a[0] = '#'
	if len(b) > 0 && b[0] == '#' {
		t.Fatal("BuiltinTOML should return a copy")
	}
}

func TestLoadMergesMarkdownStyle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.toml")
	if err := os.WriteFile(path, []byte(`
name = "custom-theme"

[markdown]
style = "light"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	th, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "custom-theme" {
		t.Fatalf("theme name = %q, want custom-theme", th.Name)
	}
	if got := th.MarkdownStyle(); got != "light" {
		t.Fatalf("markdown style = %q, want light", got)
	}
	if th.Colors.Background == "" {
		t.Fatal("load should retain default color fallback")
	}
}

func TestLoadRejectsOversizedThemeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.toml")
	body := strings.Repeat("x", int(maxThemeFileBytes)+1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized theme error, got %v", err)
	}
}
