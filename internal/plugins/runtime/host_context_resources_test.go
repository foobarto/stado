package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/pkg/tool"
)

func contextResourceTestAccess(content string) *ContextResourceAccess {
	facts := []ContextResource{
		{ID: "sha256:a", Digest: "sha256:body-a", Kind: "skill", Name: "a", Scope: "project", Provenance: "project-discovered", ModelVisible: true},
		{ID: "sha256:b", Digest: "sha256:body-b", Kind: "skill", Name: "b", Scope: "persona", Provenance: "persona-declared", ModelVisible: true, EffectiveAllowedTools: []string{"fs__read"}},
	}
	return &ContextResourceAccess{
		Catalog: func(kind string, _ tool.ToolSurfaceController) (ContextResourceSnapshot, error) {
			return ContextResourceSnapshot{Digest: "sha256:catalog", Resources: facts}, nil
		},
		Open: func(kind, id, digest string, _ tool.ToolSurfaceController) (ContextResourceContent, error) {
			if digest != "sha256:catalog" {
				return ContextResourceContent{}, context.Canceled
			}
			for _, fact := range facts {
				if fact.ID == id {
					return ContextResourceContent{ContextResource: fact, ContentFormat: "text/markdown", Content: content}, nil
				}
			}
			return ContextResourceContent{}, context.Canceled
		},
	}
}

func TestContextResourceImportsUseOperationAndKindScopedCapabilities(t *testing.T) {
	denied := newBridgeHarness(t).withCaps("context:resource:catalog:other", "context:resource:open:other")
	denied.host.ContextResources = contextResourceTestAccess("body")
	denied.install()
	request := []byte(`{"kind":"skill","limit":1}`)
	denied.memWrite(0, request)
	if got := denied.callImport(context.Background(), "stado_context_resource_catalog", 0, uint64(len(request)), 1024, 4096); got >= 0 {
		t.Fatalf("wrong-kind catalog capability returned %d", got)
	}

	h := newBridgeHarness(t).withCaps("context:resource:catalog:skill", "context:resource:open:skill")
	h.host.ContextResources = contextResourceTestAccess("body")
	h.install()
	h.memWrite(0, request)
	got := h.callImport(context.Background(), "stado_context_resource_catalog", 0, uint64(len(request)), 1024, 4096)
	if got <= 0 {
		t.Fatalf("catalog returned %d", got)
	}
	var page contextResourceCatalogResponse
	if err := json.Unmarshal(h.memRead(1024, uint32(got)), &page); err != nil {
		t.Fatal(err)
	}
	if page.Schema != contextResourceCatalogSchema || page.CatalogDigest != "sha256:catalog" || len(page.Resources) != 1 || page.Resources[0].Name != "a" || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("page = %+v", page)
	}

	second := []byte(`{"kind":"skill","offset":1,"limit":1}`)
	h.memWrite(0, second)
	if got := h.callImport(context.Background(), "stado_context_resource_catalog", 0, uint64(len(second)), 1024, 4096); got >= 0 || !strings.Contains(string(h.memRead(1024, uint32(-got))), "expected_digest") {
		t.Fatalf("unfenced second page = %d %q", got, h.memRead(1024, uint32(-got)))
	}

	open := []byte(`{"kind":"skill","id":"sha256:b","catalog_digest":"sha256:catalog"}`)
	h.memWrite(0, open)
	got = h.callImport(context.Background(), "stado_context_resource_open", 0, uint64(len(open)), 1024, 4096)
	if got <= 0 {
		t.Fatalf("open returned %d", got)
	}
	var opened contextResourceOpenResponse
	if err := json.Unmarshal(h.memRead(1024, uint32(got)), &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Schema != contextResourceOpenSchema || opened.ID != "sha256:b" || opened.Content != "body" || opened.Provenance != "persona-declared" {
		t.Fatalf("opened = %+v", opened)
	}
}

func TestSkillToolCapabilityProjectionSeparatesSearchFromLoad(t *testing.T) {
	manifest := plugins.Manifest{
		Capabilities: []string{"context:resource:catalog:skill", "context:resource:open:skill", "registry:catalog", "session:tool-surface"},
		Tools: []plugins.ToolDef{
			{Name: "skills__search", Capabilities: plugins.CapabilitySubset("context:resource:catalog:skill")},
			{Name: "skills__load", Capabilities: plugins.CapabilitySubset("context:resource:catalog:skill", "context:resource:open:skill", "registry:catalog", "session:tool-surface")},
		},
	}
	registry := &RegistryCatalogAccess{
		Snapshot: func(tool.ToolSurfaceController) (RegistryCatalogSnapshot, error) {
			return RegistryCatalogSnapshot{Digest: "sha256:registry"}, nil
		},
		Apply: func(digest string, edit tool.ToolSurfaceEdit, controller tool.ToolSurfaceController) (RegistrySurfaceEditResult, error) {
			if err := controller.ApplyToolSurface(edit); err != nil {
				return RegistrySurfaceEditResult{}, err
			}
			return RegistrySurfaceEditResult{Digest: digest, Activated: edit.Activate}, nil
		},
	}

	searchCaps, err := manifest.EffectiveToolCapabilities(manifest.Tools[0])
	if err != nil {
		t.Fatal(err)
	}
	search := newBridgeHarness(t).withCaps(searchCaps...)
	search.host.ContextResources = contextResourceTestAccess("body")
	search.host.RegistryCatalog = registry
	search.install()
	catalogRequest := []byte(`{"kind":"skill"}`)
	search.memWrite(0, catalogRequest)
	if got := search.callImport(context.Background(), "stado_context_resource_catalog", 0, uint64(len(catalogRequest)), 1024, 4096); got <= 0 {
		t.Fatalf("search catalog returned %d", got)
	}
	openRequest := []byte(`{"kind":"skill","id":"sha256:a","catalog_digest":"sha256:catalog"}`)
	search.memWrite(0, openRequest)
	if got := search.callImport(context.Background(), "stado_context_resource_open", 0, uint64(len(openRequest)), 1024, 4096); got >= 0 {
		t.Fatalf("search opened skill body with catalog-only authority: %d", got)
	}
	registryRequest := []byte(`{}`)
	search.memWrite(0, registryRequest)
	if got := search.callImport(context.Background(), "stado_registry_catalog", 0, uint64(len(registryRequest)), 1024, 4096); got >= 0 {
		t.Fatalf("search read tool registry with skill-catalog authority: %d", got)
	}
	applyRequest := []byte(`{"registry_digest":"sha256:registry","activate":["fs__read"]}`)
	search.memWrite(0, applyRequest)
	if got := search.callImport(tool.WithToolSurfaceController(context.Background(), &catalogImportSurface{}), "stado_session_tool_surface_apply", 0, uint64(len(applyRequest)), 1024, 4096); got >= 0 {
		t.Fatalf("search edited session surface with catalog-only authority: %d", got)
	}

	loadCaps, err := manifest.EffectiveToolCapabilities(manifest.Tools[1])
	if err != nil {
		t.Fatal(err)
	}
	load := newBridgeHarness(t).withCaps(loadCaps...)
	load.host.ContextResources = contextResourceTestAccess("body")
	load.host.RegistryCatalog = registry
	load.install()
	load.memWrite(0, openRequest)
	if got := load.callImport(context.Background(), "stado_context_resource_open", 0, uint64(len(openRequest)), 1024, 4096); got <= 0 {
		t.Fatalf("load open returned %d", got)
	}
	load.memWrite(0, registryRequest)
	if got := load.callImport(context.Background(), "stado_registry_catalog", 0, uint64(len(registryRequest)), 1024, 4096); got <= 0 {
		t.Fatalf("load registry catalog returned %d", got)
	}
	surface := &catalogImportSurface{}
	load.memWrite(0, applyRequest)
	if got := load.callImport(tool.WithToolSurfaceController(context.Background(), surface), "stado_session_tool_surface_apply", 0, uint64(len(applyRequest)), 1024, 4096); got <= 0 || !surface.active["fs__read"] {
		t.Fatalf("load surface apply returned %d, active=%v", got, surface.active)
	}
}

func TestContextResourceImportsRejectStaleUnknownAndHiddenFacts(t *testing.T) {
	access := contextResourceTestAccess("body")
	h := newBridgeHarness(t).withCaps("context:resource:catalog:skill", "context:resource:open:skill")
	h.host.ContextResources = access
	h.install()

	unknown := []byte(`{"kind":"skill","workflow_policy":true}`)
	h.memWrite(0, unknown)
	if got := h.callImport(context.Background(), "stado_context_resource_catalog", 0, uint64(len(unknown)), 1024, 4096); got >= 0 || !strings.Contains(string(h.memRead(1024, uint32(-got))), "unknown field") {
		t.Fatalf("unknown field = %d %q", got, h.memRead(1024, uint32(-got)))
	}
	stale := []byte(`{"kind":"skill","id":"sha256:a","catalog_digest":"sha256:stale"}`)
	h.memWrite(0, stale)
	if got := h.callImport(context.Background(), "stado_context_resource_open", 0, uint64(len(stale)), 1024, 4096); got >= 0 {
		t.Fatalf("stale open returned %d", got)
	}

	access.Catalog = func(string, tool.ToolSurfaceController) (ContextResourceSnapshot, error) {
		return ContextResourceSnapshot{Digest: "sha256:catalog", Resources: []ContextResource{{
			ID: "sha256:hidden", Digest: "sha256:hidden-body", Kind: "skill", Name: "hidden",
			Scope: "project", Provenance: "project-discovered", ModelVisible: false,
		}}}, nil
	}
	request := []byte(`{"kind":"skill"}`)
	h.memWrite(0, request)
	if got := h.callImport(context.Background(), "stado_context_resource_catalog", 0, uint64(len(request)), 1024, 4096); got >= 0 || !strings.Contains(string(h.memRead(1024, uint32(-got))), "model-hidden") {
		t.Fatalf("hidden fact = %d %q", got, h.memRead(1024, uint32(-got)))
	}
}

func TestContextResourceOpenEnvelopeFitsMaximumModelSkillBody(t *testing.T) {
	body := strings.Repeat("x", maxContextResourceContentBytes)
	h := newBridgeHarness(t).withCaps("context:resource:open:skill")
	h.host.ContextResources = contextResourceTestAccess(body)
	h.install()
	request := []byte(`{"kind":"skill","id":"sha256:a","catalog_digest":"sha256:catalog"}`)
	h.memWrite(0, request)
	// A zero output capacity asks for the required size.
	got := h.callImport(context.Background(), "stado_context_resource_open", 0, uint64(len(request)), 1024, 0)
	if got <= maxContextResourceContentBytes || got > maxContextResourceOpenBytes {
		t.Fatalf("required open response bytes = %d, want (%d,%d]", got, maxContextResourceContentBytes, maxContextResourceOpenBytes)
	}
}

func TestContextResourceOpenWorstCaseJSONEscapingFitsOneMiB(t *testing.T) {
	body := strings.Repeat("\x01", maxContextResourceContentBytes)
	h := newBridgeHarness(t).withCaps("context:resource:open:skill")
	h.host.ContextResources = contextResourceTestAccess(body)
	h.install()
	request := []byte(`{"kind":"skill","id":"sha256:a","catalog_digest":"sha256:catalog"}`)
	h.memWrite(0, request)
	got := h.callImport(context.Background(), "stado_context_resource_open", 0, uint64(len(request)), 1024, 0)
	if got <= maxContextResourceContentBytes || got > maxContextResourceOpenBytes {
		t.Fatalf("worst-case escaped response bytes = %d, want (%d,%d]", got, maxContextResourceContentBytes, maxContextResourceOpenBytes)
	}
}
