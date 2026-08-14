package plugins

import "testing"

func TestRuntimeIdentityInstalledQualifiesStableKind(t *testing.T) {
	id, err := ParseIdentity("github.com/acme/reviewer/checks@v2.1.0")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Name: "display-alias", Version: "v2.1.0"}
	runtimeID, err := RuntimeIdentityForInstalled(id, manifest, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := runtimeID.QualifiedKind("review-contract")
	if err != nil {
		t.Fatal(err)
	}
	if qualified != "github.com/acme/reviewer/checks#review-contract" {
		t.Fatalf("qualified kind = %q", qualified)
	}
	if runtimeID.Canonical != "github.com/acme/reviewer/checks@v2.1.0" || runtimeID.Namespace == manifest.Name {
		t.Fatalf("identity used display alias: %+v", runtimeID)
	}
}

func TestRuntimeIdentityRejectsForgedOrMalformedFields(t *testing.T) {
	valid := RuntimeIdentity{
		Canonical:      "github.com/acme/reviewer@v1.0.0",
		Namespace:      "github.com/acme/reviewer",
		ManifestDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	for _, mutate := range []func(*RuntimeIdentity){
		func(id *RuntimeIdentity) { id.Namespace = "stado.dev/bundled/reviewer@v1" },
		func(id *RuntimeIdentity) { id.Canonical = "github.com/other/reviewer@v1.0.0" },
		func(id *RuntimeIdentity) { id.ManifestDigest = "sha256:" + string(make([]byte, 64)) },
		func(id *RuntimeIdentity) { id.ResolvedCommit = "ZZ23456789abcdef0123456789abcdef01234567" },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid identity unexpectedly accepted: %+v", candidate)
		}
	}
}

func TestBundledAndLocalIdentityNamespacesCannotCollide(t *testing.T) {
	manifest := Manifest{Name: "stado-builtin-tool-supervise", Version: "v0.80.0"}
	bundled, err := RuntimeIdentityForBundled(manifest)
	if err != nil {
		t.Fatal(err)
	}
	local, err := RuntimeIdentityForLocal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if bundled.Namespace == local.Namespace || bundled.Namespace != "stado.dev/bundled/supervise" {
		t.Fatalf("identity namespaces collide: bundled=%q local=%q", bundled.Namespace, local.Namespace)
	}
}

func TestRuntimeIdentityForLocalSourceDoesNotUseDisplayName(t *testing.T) {
	sourceA := t.TempDir()
	sourceB := t.TempDir()
	manifest := Manifest{Name: "same-display-name", Version: "dev"}

	a, err := RuntimeIdentityForLocalSource(manifest, sourceA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RuntimeIdentityForLocalSource(manifest, sourceB)
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace == b.Namespace {
		t.Fatalf("different local sources shared namespace %q", a.Namespace)
	}

	renamed := manifest
	renamed.Name = "different-display-name"
	aRenamed, err := RuntimeIdentityForLocalSource(renamed, sourceA)
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace != aRenamed.Namespace || a.Canonical != aRenamed.Canonical {
		t.Fatalf("display rename changed source identity: before=%+v after=%+v", a, aRenamed)
	}
}

func TestRuntimeIdentityForBundledSourceDoesNotUseDisplayName(t *testing.T) {
	manifest := Manifest{Name: "presentation-one", Version: "v1.0.0"}
	one, err := RuntimeIdentityForBundledSource("reviewer", manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Name = "presentation-two"
	two, err := RuntimeIdentityForBundledSource("reviewer", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if one.Namespace != two.Namespace || one.Canonical != two.Canonical {
		t.Fatalf("display rename changed bundled source identity: before=%+v after=%+v", one, two)
	}
}

func TestRuntimeIdentityFromLockUsesSourceNotAlias(t *testing.T) {
	manifest := Manifest{Name: "local-display-name", Version: "2.1.0", WASMSHA256: "wasm-digest"}
	lock := &Lock{Entries: []LockEntry{{
		Identity: "github.com/foobarto/stado-plugins/reviewer@v2.1.0", WASMSHA256: "wasm-digest",
	}}}
	identity, err := RuntimeIdentityFromLock(lock, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Namespace != "github.com/foobarto/stado-plugins/reviewer" ||
		identity.Canonical != "github.com/foobarto/stado-plugins/reviewer@v2.1.0" {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestRuntimeIdentityFromLockRejectsAmbiguousDigest(t *testing.T) {
	manifest := Manifest{Name: "reviewer", Version: "v1.0.0", WASMSHA256: "same"}
	lock := &Lock{Entries: []LockEntry{
		{Identity: "github.com/acme/one@v1.0.0", WASMSHA256: "same"},
		{Identity: "github.com/acme/two@v1.0.0", WASMSHA256: "same"},
	}}
	if _, err := RuntimeIdentityFromLock(lock, manifest); err == nil {
		t.Fatal("ambiguous source identity unexpectedly accepted")
	}
}
