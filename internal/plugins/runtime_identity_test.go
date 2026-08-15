package plugins

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestRuntimeIdentityRejectsTaggedPackageVersionMismatch(t *testing.T) {
	id, err := ParseIdentity("github.com/acme/plugins/reviewer@v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Name: "reviewer", Version: "1.0.0"}
	if _, err := RuntimeIdentityForResolvedInstall(id, manifest, "reviewer/v2.0.0", "0123456789abcdef0123456789abcdef01234567"); err == nil || !strings.Contains(err.Error(), "declares") {
		t.Fatalf("tag/package mismatch error = %v", err)
	}
}

func TestRuntimeIdentityCommitKeepsSourceRevisionSeparateFromPackageVersion(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	id, err := ParseIdentity("github.com/acme/plugins/reviewer@" + commit)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Name: "reviewer", Version: "3.4.5", WASMSHA256: "digest"}
	entry := mustResolvedLockEntry(t, id, commit, commit, manifest)
	path := filepath.Join(t.TempDir(), "plugin-lock.toml")
	lock := &Lock{Entries: []LockEntry{entry}}
	if err := lock.Write(path); err != nil {
		t.Fatal(err)
	}
	reopened, err := ReadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := RuntimeIdentityFromLock(reopened, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if identity.SourceRevision != commit || identity.ResolvedCommit != commit || reopened.Entries[0].PackageVersion != "3.4.5" {
		t.Fatalf("identity=%#v lock=%#v", identity, reopened.Entries[0])
	}
}

func TestRuntimeIdentityLegacyCommitLockCannotFallBackToLocal(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	manifest := Manifest{Name: "reviewer", Version: "1.0.0", WASMSHA256: "digest"}
	lock := &Lock{Entries: []LockEntry{{Identity: "github.com/acme/plugins/reviewer@" + commit, WASMSHA256: "digest"}}}
	if _, err := RuntimeIdentityFromLock(lock, manifest); err == nil || !strings.Contains(err.Error(), "package_version") {
		t.Fatalf("legacy commit lock error = %v", err)
	}
}

func TestRuntimeIdentityTaggedMismatchIsNotMissingLockEvidence(t *testing.T) {
	manifest := Manifest{Name: "reviewer", Version: "1.0.0", WASMSHA256: "digest"}
	manifestDigest, err := manifest.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	lock := &Lock{Entries: []LockEntry{{
		Identity: "github.com/acme/plugins/reviewer@v2.0.0", SourceRevision: "reviewer/v2.0.0",
		ResolvedCommit: "0123456789abcdef0123456789abcdef01234567",
		PackageVersion: "2.0.0", ManifestDigest: manifestDigest, WASMSHA256: "digest",
	}}}
	_, err = RuntimeIdentityFromLock(lock, manifest)
	if err == nil || errors.Is(err, ErrRuntimeIdentityNotFound) {
		t.Fatalf("tag mismatch must fail closed rather than permit local fallback: %v", err)
	}
}

func TestRuntimeIdentityFromLockPreservesMonorepoSourceRevision(t *testing.T) {
	manifest := Manifest{Name: "reviewer", Version: "1.2.0", WASMSHA256: "digest"}
	id, err := ParseIdentity("github.com/acme/plugins/reviewer@v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	entry := mustResolvedLockEntry(t, id, "reviewer/v1.2.0", "0123456789abcdef0123456789abcdef01234567", manifest)
	lock := &Lock{Entries: []LockEntry{entry}}
	identity, err := RuntimeIdentityFromLock(lock, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if identity.SourceRevision != "reviewer/v1.2.0" || identity.Namespace != "github.com/acme/plugins/reviewer" {
		t.Fatalf("identity = %#v", identity)
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
		func(id *RuntimeIdentity) { id.ResolvedCommit = "0123456789abcdef0123456789abcdef01234567" },
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
	id, err := ParseIdentity("github.com/foobarto/stado-plugins/reviewer@v2.1.0")
	if err != nil {
		t.Fatal(err)
	}
	entry := mustResolvedLockEntry(t, id, "reviewer/v2.1.0", "0123456789abcdef0123456789abcdef01234567", manifest)
	lock := &Lock{Entries: []LockEntry{entry}}
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

func TestRuntimeIdentityFromLockRejectsDuplicateIdentityEvidence(t *testing.T) {
	manifest := Manifest{Name: "reviewer", Version: "v1.0.0", WASMSHA256: "same"}
	entry := LockEntry{
		Identity: "github.com/acme/plugins/reviewer@v1.0.0", SourceRevision: "reviewer/v1.0.0",
		ResolvedCommit: "0123456789abcdef0123456789abcdef01234567",
		PackageVersion: "v1.0.0", WASMSHA256: "same",
	}
	manifestDigest, err := manifest.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	entry.ManifestDigest = manifestDigest
	lock := &Lock{Entries: []LockEntry{entry, entry}}
	if _, err := RuntimeIdentityFromLock(lock, manifest); err == nil {
		t.Fatal("duplicate lock evidence unexpectedly accepted")
	}
}

func TestRuntimeIdentityFromLockRejectsSameWASMManifestRewrite(t *testing.T) {
	id, err := ParseIdentity("github.com/acme/plugins/reviewer@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	locked := Manifest{Name: "reviewer", Version: "v1.0.0", Author: "original", WASMSHA256: "same"}
	entry := mustResolvedLockEntry(t, id, "reviewer/v1.0.0", "0123456789abcdef0123456789abcdef01234567", locked)
	rewritten := locked
	rewritten.Author = "replacement"
	if _, err := RuntimeIdentityFromLock(&Lock{Entries: []LockEntry{entry}}, rewritten); err == nil || !strings.Contains(err.Error(), "canonical manifest digest") {
		t.Fatalf("manifest rewrite error = %v", err)
	}
}

func mustResolvedLockEntry(t *testing.T, id Identity, sourceRevision, resolvedCommit string, manifest Manifest) LockEntry {
	t.Helper()
	entry, err := LockEntryFromResolvedManifest(id, sourceRevision, resolvedCommit, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
