package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/pkg/tool"
)

type catalogImportSurface struct {
	active     map[string]bool
	applyCalls int
}

func (s *catalogImportSurface) AllowsToolSurface(string) bool { return true }
func (s *catalogImportSurface) ApplyToolSurface(edit tool.ToolSurfaceEdit) error {
	s.applyCalls++
	if s.active == nil {
		s.active = make(map[string]bool)
	}
	for _, name := range edit.Activate {
		s.active[name] = true
	}
	for _, name := range edit.Deactivate {
		delete(s.active, name)
	}
	return nil
}

func TestRegistrySurfaceEditAllows4096NamesAndRejectsLargerBatchAtomically(t *testing.T) {
	surface := &catalogImportSurface{}
	access := &RegistryCatalogAccess{
		Apply: func(digest string, edit tool.ToolSurfaceEdit, controller tool.ToolSurfaceController) (RegistrySurfaceEditResult, error) {
			if digest != "exact-digest" {
				return RegistrySurfaceEditResult{}, context.Canceled
			}
			if err := controller.ApplyToolSurface(edit); err != nil {
				return RegistrySurfaceEditResult{}, err
			}
			return RegistrySurfaceEditResult{Digest: digest, Activated: edit.Activate, Deactivated: edit.Deactivate}, nil
		},
	}
	h := newBridgeHarness(t).withCaps("session:tool-surface")
	h.host.RegistryCatalog = access
	h.install()
	ctx := tool.WithToolSurfaceController(context.Background(), surface)

	names := make([]string, maxRegistrySurfaceEditNames+1)
	for i := range names {
		names[i] = fmt.Sprintf("n%04d", i)
	}
	request, err := json.Marshal(registrySurfaceEditRequest{RegistryDigest: "exact-digest", Activate: names[:maxRegistrySurfaceEditNames]})
	if err != nil {
		t.Fatal(err)
	}
	h.memWrite(0, request)
	if got := h.callImport(ctx, "stado_session_tool_surface_apply", 0, uint64(len(request)), 0, 65536); got <= 0 {
		t.Fatalf("4096-name surface apply returned %d", got)
	}
	if surface.applyCalls != 1 || len(surface.active) != maxRegistrySurfaceEditNames {
		t.Fatalf("4096-name apply calls=%d active=%d", surface.applyCalls, len(surface.active))
	}

	request, err = json.Marshal(registrySurfaceEditRequest{RegistryDigest: "exact-digest", Activate: names})
	if err != nil {
		t.Fatal(err)
	}
	h.memWrite(0, request)
	if got := h.callImport(ctx, "stado_session_tool_surface_apply", 0, uint64(len(request)), 0, 65536); got >= 0 {
		t.Fatalf("4097-name surface apply returned %d", got)
	}
	if surface.applyCalls != 1 || len(surface.active) != maxRegistrySurfaceEditNames {
		t.Fatalf("oversize edit mutated surface: calls=%d active=%d", surface.applyCalls, len(surface.active))
	}

	if got := h.callImport(ctx, "stado_session_tool_surface_apply", 0, maxRegistryCatalogRequestBytes+1, 0, 65536); got >= 0 {
		t.Fatalf("request over the 1 MiB ceiling returned %d", got)
	}
	if surface.applyCalls != 1 || len(surface.active) != maxRegistrySurfaceEditNames {
		t.Fatalf("oversize request mutated surface: calls=%d active=%d", surface.applyCalls, len(surface.active))
	}
}

func TestRegistryCatalogImportsCapabilityPaginationAndSurfaceBinding(t *testing.T) {
	surface := &catalogImportSurface{}
	access := &RegistryCatalogAccess{
		Snapshot: func(tool.ToolSurfaceController) (RegistryCatalogSnapshot, error) {
			return RegistryCatalogSnapshot{Digest: "exact-digest", Tools: []RegistryCatalogTool{
				{Name: "a", Canonical: "demo.a", Plugin: "demo", Description: "A", Schema: json.RawMessage(`{"type":"object"}`), Class: "non-mutating", SourceNamespace: "source/a"},
				{Name: "b", Canonical: "demo.b", Plugin: "demo", Description: "B", Schema: json.RawMessage(`{"type":"object"}`), Class: "exec", SourceNamespace: "source/b"},
			}}, nil
		},
		Apply: func(digest string, edit tool.ToolSurfaceEdit, controller tool.ToolSurfaceController) (RegistrySurfaceEditResult, error) {
			if digest != "exact-digest" {
				return RegistrySurfaceEditResult{}, context.Canceled
			}
			if err := controller.ApplyToolSurface(edit); err != nil {
				return RegistrySurfaceEditResult{}, err
			}
			return RegistrySurfaceEditResult{Digest: digest, Activated: edit.Activate, Deactivated: edit.Deactivate}, nil
		},
	}

	denied := newBridgeHarness(t)
	denied.host.RegistryCatalog = access
	denied.install()
	denied.memWrite(0, []byte(`{"limit":1}`))
	if got := denied.callImport(context.Background(), "stado_registry_catalog", 0, 11, 1024, 4096); got >= 0 {
		t.Fatalf("catalog without capability returned %d", got)
	}

	h := newBridgeHarness(t).withCaps("registry:catalog", "session:tool-surface")
	h.host.RegistryCatalog = access
	h.install()
	request := []byte(`{"limit":1}`)
	h.memWrite(0, request)
	got := h.callImport(context.Background(), "stado_registry_catalog", 0, uint64(len(request)), 1024, 4096)
	if got <= 0 {
		t.Fatalf("catalog returned %d", got)
	}
	var page registryCatalogResponse
	if err := json.Unmarshal(h.memRead(1024, uint32(got)), &page); err != nil {
		t.Fatal(err)
	}
	if page.RegistryDigest != "exact-digest" || len(page.Tools) != 1 || page.Tools[0].Name != "a" || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("page = %+v", page)
	}

	edit := []byte(`{"registry_digest":"exact-digest","activate":["b"]}`)
	h.memWrite(0, edit)
	ctx := tool.WithToolSurfaceController(context.Background(), surface)
	got = h.callImport(ctx, "stado_session_tool_surface_apply", 0, uint64(len(edit)), 1024, 4096)
	if got <= 0 || !surface.active["b"] {
		t.Fatalf("surface apply returned %d, active=%v", got, surface.active)
	}
}

func TestToolRegistryPerToolCapabilitiesKeepReadOnlyToolsFromSurfaceMutation(t *testing.T) {
	manifest := plugins.Manifest{
		Capabilities: []string{"registry:catalog", "session:tool-surface"},
		Tools: []plugins.ToolDef{
			{Name: "tools__search", Capabilities: plugins.CapabilitySubset("registry:catalog")},
			{Name: "tools__activate", Capabilities: plugins.CapabilitySubset("registry:catalog", "session:tool-surface")},
		},
	}
	access := &RegistryCatalogAccess{
		Snapshot: func(tool.ToolSurfaceController) (RegistryCatalogSnapshot, error) {
			return RegistryCatalogSnapshot{Digest: "exact-digest"}, nil
		},
		Apply: func(digest string, edit tool.ToolSurfaceEdit, controller tool.ToolSurfaceController) (RegistrySurfaceEditResult, error) {
			if err := controller.ApplyToolSurface(edit); err != nil {
				return RegistrySurfaceEditResult{}, err
			}
			return RegistrySurfaceEditResult{Digest: digest, Activated: edit.Activate}, nil
		},
	}
	request := []byte(`{"registry_digest":"exact-digest","activate":["fs__read"]}`)

	readCaps, err := manifest.EffectiveToolCapabilities(manifest.Tools[0])
	if err != nil {
		t.Fatal(err)
	}
	readOnly := newBridgeHarness(t).withCaps(readCaps...)
	readOnly.host.RegistryCatalog = access
	readOnly.install()
	readOnly.memWrite(0, request)
	if got := readOnly.callImport(tool.WithToolSurfaceController(context.Background(), &catalogImportSurface{}), "stado_session_tool_surface_apply", 0, uint64(len(request)), 1024, 4096); got >= 0 {
		t.Fatalf("read-only tool edited session surface: %d", got)
	}

	mutatingCaps, err := manifest.EffectiveToolCapabilities(manifest.Tools[1])
	if err != nil {
		t.Fatal(err)
	}
	mutating := newBridgeHarness(t).withCaps(mutatingCaps...)
	mutating.host.RegistryCatalog = access
	mutating.install()
	surface := &catalogImportSurface{}
	mutating.memWrite(0, request)
	if got := mutating.callImport(tool.WithToolSurfaceController(context.Background(), surface), "stado_session_tool_surface_apply", 0, uint64(len(request)), 1024, 4096); got <= 0 || !surface.active["fs__read"] {
		t.Fatalf("mutating tool apply = %d, active=%v", got, surface.active)
	}
}

func TestRegistryCatalogImportsRejectUnknownFieldsAndMissingSession(t *testing.T) {
	access := &RegistryCatalogAccess{Snapshot: func(tool.ToolSurfaceController) (RegistryCatalogSnapshot, error) {
		return RegistryCatalogSnapshot{Digest: "d"}, nil
	}, Apply: func(string, tool.ToolSurfaceEdit, tool.ToolSurfaceController) (RegistrySurfaceEditResult, error) {
		return RegistrySurfaceEditResult{}, nil
	}}
	h := newBridgeHarness(t).withCaps("registry:catalog", "session:tool-surface")
	h.host.RegistryCatalog = access
	h.install()
	bad := []byte(`{"limit":1,"workflow_policy":true}`)
	h.memWrite(0, bad)
	got := h.callImport(context.Background(), "stado_registry_catalog", 0, uint64(len(bad)), 1024, 4096)
	if got >= 0 || !strings.Contains(string(h.memRead(1024, uint32(-got))), "unknown field") {
		t.Fatalf("unknown-field result = %d %q", got, h.memRead(1024, uint32(-got)))
	}
	edit := []byte(`{"registry_digest":"d","activate":["a"]}`)
	h.memWrite(0, edit)
	if got := h.callImport(context.Background(), "stado_session_tool_surface_apply", 0, uint64(len(edit)), 1024, 4096); got >= 0 {
		t.Fatalf("surface edit without session controller returned %d", got)
	}
}
