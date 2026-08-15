package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// catStubTool is a minimal installed-plugin stand-in for catalog projection.
type catStubTool struct {
	name     string
	metadata ToolMetadata
}

func (s catStubTool) Name() string               { return s.name }
func (s catStubTool) Description() string        { return "search GTFOBins for a binary" }
func (s catStubTool) Schema() map[string]any     { return map[string]any{"type": "object"} }
func (s catStubTool) ToolMetadata() ToolMetadata { return s.metadata }
func (catStubTool) Run(context.Context, json.RawMessage, pkgtool.Host) (pkgtool.Result, error) {
	return pkgtool.Result{Content: "ok"}, nil
}

// TestToolMetadataFor_InstalledCategories regresses EP-0037 §C: an
// installed plugin declares per-tool `categories` (+ `extra_categories`) in
// its manifest and the concrete registry entry carries that taxonomy.
func TestToolMetadataFor_InstalledCategories(t *testing.T) {
	const toolName = "gtfobins__search"
	tdef := plugins.ToolDef{
		Name: toolName, Categories: []string{"security"}, ExtraCategories: []string{"gtfobins"},
	}
	md := ToolMetadataFor(&installedPluginTool{manifest: plugins.Manifest{Name: "gtfobins"}, def: tdef})
	// Canonical and extra must stay separate: tools.categories and
	// [tools].autoload_categories trust Categories as the canonical taxonomy,
	// so a free-form extra ("gtfobins") must NOT leak into Categories.
	if len(md.Categories) != 1 || md.Categories[0] != "security" {
		t.Errorf("Categories = %v; want canonical-only [security]", md.Categories)
	}
	if len(md.ExtraCategories) != 1 || md.ExtraCategories[0] != "gtfobins" {
		t.Errorf("ExtraCategories = %v; want [gtfobins]", md.ExtraCategories)
	}
}

// TestRegistryCatalogCarriesInstalledCategoryFactsSeparately ensures the
// generic native projection preserves signed canonical and extra category
// facts without taking ownership of search, grouping, or display policy.
func TestRegistryCatalogCarriesInstalledCategoryFactsSeparately(t *testing.T) {
	const toolName = "gtfobins__search"
	reg := tools.NewRegistry()
	reg.Register(catStubTool{name: toolName, metadata: ToolMetadata{
		Canonical:        "gtfobins.search",
		Plugin:           "gtfobins",
		PackageNamespace: "github.com/example/plugins/gtfobins",
		Categories:       []string{"security"}, ExtraCategories: []string{"gtfobins"},
	}})
	access := NewRegistryCatalogAccess(reg, "github.com/example/plugins/catalog")
	snapshot, err := access.Snapshot(nil)
	if err != nil {
		t.Fatalf("catalog snapshot: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		t.Fatalf("catalog tools = %+v", snapshot.Tools)
	}
	got := snapshot.Tools[0]
	if len(got.Categories) != 1 || got.Categories[0] != "security" {
		t.Fatalf("categories = %v", got.Categories)
	}
	if len(got.ExtraCategories) != 1 || got.ExtraCategories[0] != "gtfobins" {
		t.Fatalf("extra categories = %v", got.ExtraCategories)
	}
}
