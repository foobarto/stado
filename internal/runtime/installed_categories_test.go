package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// catStubTool is a minimal installed-plugin stand-in: its Name() matches the
// key under which we inject the manifest into installedByTool.
type catStubTool struct{ name string }

func (s catStubTool) Name() string           { return s.name }
func (s catStubTool) Description() string    { return "search GTFOBins for a binary" }
func (s catStubTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (catStubTool) Run(context.Context, json.RawMessage, pkgtool.Host) (pkgtool.Result, error) {
	return pkgtool.Result{Content: "ok"}, nil
}

// withInstalledManifest injects a manifest into the package-global
// installedByTool map (mirroring registerInstalledPluginTools) and restores
// it on cleanup, so the test doesn't leak into sibling tests that share the
// global.
func withInstalledManifest(t *testing.T, toolName string, mf plugins.Manifest) {
	t.Helper()
	installedRegistryMu.Lock()
	prev, had := installedByTool[toolName]
	installedByTool[toolName] = installedRecord{Manifest: mf}
	installedRegistryMu.Unlock()
	t.Cleanup(func() {
		installedRegistryMu.Lock()
		if had {
			installedByTool[toolName] = prev
		} else {
			delete(installedByTool, toolName)
		}
		installedRegistryMu.Unlock()
	})
}

// TestLookupToolMetadata_InstalledCategories regresses EP-0037 §C: an
// installed plugin declares per-tool `categories` (+ `extra_categories`) in
// its manifest, validated at install time — but LookupToolMetadata never
// read them back, so tools.categories / tools.in_category / tools.search all
// behaved as if installed tools had no categories. The taxonomy was parsed
// and then dead at runtime.
func TestLookupToolMetadata_InstalledCategories(t *testing.T) {
	const toolName = "gtfobins__search"
	withInstalledManifest(t, toolName, plugins.Manifest{
		Name: "gtfobins",
		Tools: []plugins.ToolDef{{
			Name:            toolName,
			Categories:      []string{"security"},
			ExtraCategories: []string{"gtfobins"},
		}},
	})

	got := LookupToolMetadata(toolName).Categories
	want := map[string]bool{"security": true, "gtfobins": true}
	if len(got) != len(want) {
		t.Fatalf("categories = %v; want canonical+extra %v", got, want)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected category %q (want only %v)", c, want)
		}
	}
}

// TestMetaSearch_MatchesInstalledCategory regresses the search-haystack half
// of the same finding: tools.search only matched name+description, so a query
// naming a category (the natural way an agent narrows the surface) never
// surfaced the tool even once the category was readable.
func TestMetaSearch_MatchesInstalledCategory(t *testing.T) {
	const toolName = "gtfobins__search"
	withInstalledManifest(t, toolName, plugins.Manifest{
		Name: "gtfobins",
		Tools: []plugins.ToolDef{{
			Name:       toolName,
			Categories: []string{"security"},
		}},
	})

	reg := tools.NewRegistry()
	reg.Register(catStubTool{name: toolName})
	m := &metaSearch{reg: reg}

	// "security" appears in neither the name nor the description — only the
	// category. Pre-fix this returns zero tools.
	res, err := m.Run(context.Background(), []byte(`{"query":"security"}`), nil)
	if err != nil {
		t.Fatalf("metaSearch.Run: %v", err)
	}
	if !strings.Contains(res.Content, toolName) {
		t.Fatalf("search for category 'security' should surface %q; got %s", toolName, res.Content)
	}
}
