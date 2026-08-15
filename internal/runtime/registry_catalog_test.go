package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

type registryCatalogStub struct {
	name        string
	description string
	schema      map[string]any
	metadata    ToolMetadata
}

func (s registryCatalogStub) Name() string { return s.name }
func (s registryCatalogStub) Description() string {
	if s.description != "" {
		return s.description
	}
	return "catalog stub " + s.name
}
func (s registryCatalogStub) Schema() map[string]any {
	if s.schema != nil {
		return s.schema
	}
	return map[string]any{"type": "object"}
}
func (s registryCatalogStub) Class() tool.Class          { return tool.ClassNonMutating }
func (s registryCatalogStub) ToolMetadata() ToolMetadata { return s.metadata }
func (s registryCatalogStub) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	return tool.Result{}, nil
}

type registryCatalogSurface struct {
	denied     map[string]bool
	active     map[string]bool
	applyCalls int
}

func (s *registryCatalogSurface) AllowsToolSurface(name string) bool {
	return s != nil && !s.denied[name]
}

func (s *registryCatalogSurface) ApplyToolSurface(edit tool.ToolSurfaceEdit) error {
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

func TestRegistryCatalogExactProjectionAndAtomicSurfaceEdits(t *testing.T) {
	const caller = "github.com/example/catalog"
	reg := tools.NewRegistry()
	metadata := func(name, plugin, namespace string) ToolMetadata {
		return ToolMetadata{Canonical: CanonicalToolName(name), Plugin: plugin, PackageNamespace: namespace, Categories: []string{"filesystem"}}
	}
	reg.Register(registryCatalogStub{name: "fs__read", metadata: metadata("fs__read", "filesystem", "stado.dev/bundled/fs")})
	reg.Register(registryCatalogStub{name: "fs__write", metadata: metadata("fs__write", "filesystem", "stado.dev/bundled/fs")})
	reg.Register(registryCatalogStub{name: "catalog__search", metadata: metadata("catalog__search", "catalog", caller)})
	qualityMetadata := metadata("quality__decide", "quality", "github.com/example/quality")
	qualityMetadata.LifecycleApplication = "github.com/example/quality"
	reg.Register(registryCatalogStub{name: "quality__decide", metadata: qualityMetadata})
	reg.Register(registryCatalogStub{name: "native_debt"})

	surface := &registryCatalogSurface{denied: map[string]bool{"fs__write": true}, active: map[string]bool{}}
	access := NewRegistryCatalogAccess(reg, caller)
	snapshot, err := access.Snapshot(surface)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != "fs__read" {
		t.Fatalf("catalog = %+v; want only exact permitted non-caller, non-lifecycle WASM tool", snapshot.Tools)
	}
	if snapshot.Tools[0].SourceNamespace != "stado.dev/bundled/fs" {
		t.Fatalf("source namespace = %q", snapshot.Tools[0].SourceNamespace)
	}
	if snapshot.Tools[0].Canonical != "fs.read" || snapshot.Tools[0].Plugin != "filesystem" {
		t.Fatalf("canonical/display facts = %q/%q", snapshot.Tools[0].Canonical, snapshot.Tools[0].Plugin)
	}

	if _, err := access.Apply(snapshot.Digest, tool.ToolSurfaceEdit{Activate: []string{"fs__read"}}, surface); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !surface.active["fs__read"] {
		t.Fatal("activation was not applied")
	}
	if _, err := access.Apply(snapshot.Digest, tool.ToolSurfaceEdit{Deactivate: []string{"fs__read"}}, surface); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if surface.active["fs__read"] {
		t.Fatal("deactivation was not applied")
	}

	// Allows means the current ceiling, not current activation. A permitted
	// deactivated tool remains catalogued and can be activated again.
	fresh, err := access.Snapshot(surface)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Tools) != 1 || fresh.Tools[0].Name != "fs__read" {
		t.Fatalf("deactivated tool disappeared from catalog: %+v", fresh.Tools)
	}
	if _, err := access.Apply(fresh.Digest, tool.ToolSurfaceEdit{Activate: []string{"fs__read"}}, surface); err != nil {
		t.Fatalf("reactivate: %v", err)
	}

	beforeCalls := surface.applyCalls
	if _, err := access.Apply(fresh.Digest, tool.ToolSurfaceEdit{Activate: []string{"fs__read", "missing"}}, surface); err == nil {
		t.Fatal("unknown target was accepted")
	}
	if surface.applyCalls != beforeCalls {
		t.Fatalf("invalid batch reached mutation controller: calls %d -> %d", beforeCalls, surface.applyCalls)
	}
	if _, err := access.Apply(fresh.Digest, tool.ToolSurfaceEdit{Activate: []string{"fs__write"}}, surface); err == nil {
		t.Fatal("session-disabled target was accepted")
	}
}

func TestRegistryCatalogDigestFencesRegistryInstanceAndRevision(t *testing.T) {
	makeRegistry := func() *tools.Registry {
		reg := tools.NewRegistry()
		reg.Register(registryCatalogStub{name: "fs__read", metadata: ToolMetadata{Canonical: "fs.read", Plugin: "filesystem", PackageNamespace: "stado.dev/bundled/fs"}})
		return reg
	}
	surface := &registryCatalogSurface{}
	first := makeRegistry()
	firstAccess := NewRegistryCatalogAccess(first, "github.com/example/caller")
	snapshot, err := firstAccess.Snapshot(surface)
	if err != nil {
		t.Fatal(err)
	}
	secondAccess := NewRegistryCatalogAccess(makeRegistry(), "github.com/example/caller")
	second, err := secondAccess.Snapshot(surface)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Digest == second.Digest {
		t.Fatal("distinct registry instances produced the same freshness digest")
	}
	first.Register(registryCatalogStub{name: "fs__grep", metadata: ToolMetadata{Canonical: "fs.grep", Plugin: "filesystem", PackageNamespace: "stado.dev/bundled/fs"}})
	if _, err := firstAccess.Apply(snapshot.Digest, tool.ToolSurfaceEdit{Activate: []string{"fs__read"}}, surface); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale revision apply error = %v", err)
	}
}

func TestRegistryCatalogRejectsOversizeFacts(t *testing.T) {
	base := func() registryCatalogStub {
		return registryCatalogStub{name: "fs__read", metadata: ToolMetadata{
			Canonical: "fs.read", Plugin: "filesystem", PackageNamespace: "stado.dev/bundled/fs",
		}}
	}
	tests := []struct {
		name string
		want string
		edit func(*registryCatalogStub)
	}{
		{"wire name", "tool name", func(s *registryCatalogStub) { s.name = strings.Repeat("n", maxRegistryCatalogToolNameBytes+1) }},
		{"canonical", "canonical name", func(s *registryCatalogStub) {
			s.metadata.Canonical = strings.Repeat("c", maxRegistryCatalogCanonicalBytes+1)
		}},
		{"plugin display", "plugin display name", func(s *registryCatalogStub) { s.metadata.Plugin = strings.Repeat("p", maxRegistryCatalogPluginBytes+1) }},
		{"description", "description", func(s *registryCatalogStub) {
			s.description = strings.Repeat("d", maxRegistryCatalogDescriptionBytes+1)
		}},
		{"schema", "schema", func(s *registryCatalogStub) {
			s.schema = map[string]any{"value": strings.Repeat("s", maxRegistryCatalogSchemaBytes+1)}
		}},
		{"source namespace", "source namespace", func(s *registryCatalogStub) {
			s.metadata.PackageNamespace = strings.Repeat("x", maxRegistryCatalogNamespaceBytes+1)
		}},
		{"category count", "categories", func(s *registryCatalogStub) {
			s.metadata.Categories = make([]string, maxRegistryCatalogCategories+1)
			for i := range s.metadata.Categories {
				s.metadata.Categories[i] = "filesystem"
			}
		}},
		{"category bytes", "category", func(s *registryCatalogStub) {
			s.metadata.Categories = []string{strings.Repeat("x", maxRegistryCatalogCategoryBytes+1)}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base()
			tc.edit(&candidate)
			reg := tools.NewRegistry()
			reg.Register(candidate)
			_, err := NewRegistryCatalogAccess(reg, "github.com/example/caller").Snapshot(nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
