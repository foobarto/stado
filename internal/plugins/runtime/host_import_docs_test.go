package runtime

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestEveryLiveHostImportIsDocumented keeps the operator/plugin-author ABI
// reference synchronized with the module that wazero actually links. The
// registry is deliberately enumerated from a real Host rather than copied into
// a second name table: adding a new Export("stado_...") without documenting
// it must fail CI in the same change.
func TestEveryLiveHostImportIsDocumented(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	// cfg:state_dir is the only capability-shaped symbol currently omitted
	// from the host module when its exact capability is absent. Include it so
	// the test enumerates the complete live ABI rather than one guest's view.
	host := NewHost(plugins.Manifest{
		Name:         "host-import-docs",
		Version:      "0.0.0",
		Capabilities: []string{"cfg:state_dir"},
	}, t.TempDir(), nil)
	if err := InstallHostImports(ctx, rt, host); err != nil {
		t.Fatal(err)
	}
	module := rt.Wazero().Module(NamespaceStado)
	if module == nil {
		t.Fatal("stado host module was not installed")
	}

	docPath := filepath.Join("..", "..", "..", "docs", "plugins", "host-imports.md")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	documented := make(map[string]bool)
	for _, name := range regexp.MustCompile(`\bstado_[a-z0-9_]+\b`).FindAllString(string(doc), -1) {
		documented[name] = true
	}
	missing := make([]string, 0)
	for name := range module.ExportedFunctionDefinitions() {
		if !documented[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("live host imports missing from %s: %v", docPath, missing)
	}
}
