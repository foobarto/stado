package theme

import "testing"

func TestCatalogLoadsBundledThemes(t *testing.T) {
	entries := Catalog()
	if len(entries) < 3 {
		t.Fatalf("catalog size = %d, want at least 3", len(entries))
	}
	seenLight := false
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
	}
	if !seenLight {
		t.Fatal("catalog should include a light theme")
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
