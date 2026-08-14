package artifacts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

func customIdentity(t *testing.T, version string, manifest plugins.Manifest) plugins.RuntimeIdentity {
	t.Helper()
	id, err := plugins.ParseIdentity("github.com/foobarto/stado-plugins/reviewer@" + version)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID, err := plugins.RuntimeIdentityForInstalled(id, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	return runtimeID
}

func TestPluginDefinedKindArchivesDescriptorBeforeArtifact(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, consumer := authority.New(store)
	registry := NewKindRegistry()
	definition := plugins.ArtifactKindDef{
		Name:   "review-contract",
		Schema: `{"type":"object","additionalProperties":false,"required":["objective"],"properties":{"objective":{"type":"string","minLength":1}}}`,
		Index:  []plugins.ArtifactIndexProjection{{Pointer: "/objective", Role: "title"}},
	}
	manifest := plugins.Manifest{Name: "reviewer", Version: "v1.0.0", ArtifactKinds: []plugins.ArtifactKindDef{definition}}
	identity := customIdentity(t, "v1.0.0", manifest)
	if err := registry.Register(identity, manifest.ArtifactKinds); err != nil {
		t.Fatal(err)
	}
	kindName, _ := identity.QualifiedKind(definition.Name)
	svc := NewServiceWithKinds(store, consumer, registry)
	created, err := svc.Create(context.Background(), Artifact{
		Kind: Kind(kindName), Scope: ScopeRepo,
		Binding: ScopeBinding{CanonicalRepoID: "repo:1"},
		Data:    json.RawMessage(`{"objective":"review every turn"}`),
	}, "alice", "plugin:reviewer", "create")
	if err != nil {
		t.Fatal(err)
	}
	if created.KindSchema.PluginIdentity != identity.Canonical || created.KindSchema.SchemaDigest != definition.SchemaDigest() {
		t.Fatalf("kind schema not host-bound: %+v", created.KindSchema)
	}
	records := store.Records()
	if len(records) != 1 || len(records[0].Transaction.Events) != 2 ||
		records[0].Transaction.Events[0].Type != "kind.registered" ||
		records[0].Transaction.Events[1].Type != "artifact.create" {
		t.Fatalf("descriptor was not archived atomically before artifact: %+v", records)
	}

	if _, err := svc.Create(context.Background(), Artifact{
		Kind: Kind(kindName), Scope: ScopeGlobal, Data: json.RawMessage(`{"objective":"","extra":true}`),
	}, "alice", "plugin:reviewer", "invalid"); err == nil {
		t.Fatal("schema-invalid custom artifact accepted")
	}
}

func TestIndexRebuildUsesArchivedSchemaAcrossPluginUpgrade(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, consumer := authority.New(store)
	registry := NewKindRegistry()

	v1 := plugins.ArtifactKindDef{
		Name: "report", Schema: `{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`,
		Index: []plugins.ArtifactIndexProjection{{Pointer: "/title", Role: "title"}},
	}
	m1 := plugins.Manifest{Name: "reviewer", Version: "v1.0.0", ArtifactKinds: []plugins.ArtifactKindDef{v1}}
	id1 := customIdentity(t, "v1.0.0", m1)
	if err := registry.Register(id1, m1.ArtifactKinds); err != nil {
		t.Fatal(err)
	}
	qualified, _ := id1.QualifiedKind("report")
	svc := NewServiceWithKinds(store, consumer, registry)
	if _, err := svc.Create(context.Background(), Artifact{Kind: Kind(qualified), Scope: ScopeGlobal, Data: json.RawMessage(`{"title":"ancient schema marker"}`)}, "alice", "v1", "v1"); err != nil {
		t.Fatal(err)
	}

	v2 := plugins.ArtifactKindDef{
		Name: "report", Schema: `{"type":"object","required":["heading"],"properties":{"heading":{"type":"string"}}}`,
		Index: []plugins.ArtifactIndexProjection{{Pointer: "/heading", Role: "title"}},
	}
	m2 := plugins.Manifest{Name: "reviewer", Version: "v2.0.0", ArtifactKinds: []plugins.ArtifactKindDef{v2}}
	id2 := customIdentity(t, "v2.0.0", m2)
	if err := registry.Register(id2, m2.ArtifactKinds); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), Artifact{Kind: Kind(qualified), Scope: ScopeGlobal, Data: json.RawMessage(`{"heading":"future schema marker"}`)}, "alice", "v2", "v2"); err != nil {
		t.Fatal(err)
	}

	idx, err := OpenIndex(filepath.Join(t.TempDir(), "artifact-index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Rebuild(store.Records()); err != nil {
		t.Fatal(err)
	}
	query := Query{Context: QueryContext{Principal: "alice"}}
	for _, marker := range []string{"ancient", "future"} {
		found, err := idx.Search(context.Background(), svc, marker, query)
		if err != nil || len(found) != 1 {
			t.Fatalf("search %q: found=%v err=%v", marker, found, err)
		}
	}
}
