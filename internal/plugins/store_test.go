package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const storeTestCommit = "0123456789abcdef0123456789abcdef01234567"

func writeStoreTestPackage(t *testing.T, root string, record InstallRecord, manifest Manifest) string {
	t.Helper()
	dir := filepath.Join(root, record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("test-signature"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("same-wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallRecord(dir, record, manifest); err != nil {
		t.Fatal(err)
	}
	return dir
}

func storeTestManifest() Manifest {
	wasm := sha256.Sum256([]byte("same-wasm"))
	return Manifest{
		Name: "friendly", Version: "1.0.0", Author: "tester",
		AuthorPubkeyFpr: strings.Repeat("a", 64), WASMSHA256: hex.EncodeToString(wasm[:]),
	}
}

func addRemoteStoreTestPackage(t *testing.T, root, rawIdentity string, manifest Manifest) InstalledPackage {
	t.Helper()
	id, err := ParseIdentity(rawIdentity)
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewRemoteInstallRecord(id, id.Version, storeTestCommit, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if id.IsCommit() {
		record, err = NewRemoteInstallRecord(id, id.Version, id.Version, manifest)
		if err != nil {
			t.Fatal(err)
		}
	}
	writeStoreTestPackage(t, root, record, manifest)
	lockPath := filepath.Join(filepath.Dir(root), "plugin-lock.toml")
	lock, err := ReadLock(lockPath)
	if os.IsNotExist(err) {
		lock = NewLock()
	} else if err != nil {
		t.Fatal(err)
	}
	entry, err := LockEntryFromResolvedManifest(id, record.SourceRevision, record.ResolvedCommit, manifest)
	if err != nil {
		t.Fatal(err)
	}
	lock.Add(entry)
	if err := lock.Write(lockPath); err != nil {
		t.Fatal(err)
	}
	packages, err := ListInstalledPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		if pkg.Record.StoreKey == record.StoreKey {
			return pkg
		}
	}
	t.Fatal("new package not found")
	return InstalledPackage{}
}

func TestSourceKeyedStoreAllowsSamePackageShapeFromDistinctRemoteSources(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	manifest := storeTestManifest()
	one := addRemoteStoreTestPackage(t, root, "github.com/one/repo@v1.0.0", manifest)
	two := addRemoteStoreTestPackage(t, root, "github.com/two/repo@v1.0.0", manifest)
	if one.Record.StoreKey == two.Record.StoreKey || one.Identity.Namespace == two.Identity.Namespace {
		t.Fatal("distinct sources collapsed to one installed authority")
	}
	packages, err := ListInstalledPackages(root)
	if err != nil || len(packages) != 2 {
		t.Fatalf("packages=%+v err=%v", packages, err)
	}
	if _, err := ResolveInstalledPackage([]string{root}, manifest.Name); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("friendly alias did not fail closed: %v", err)
	}
	got, err := ResolveInstalledPackage([]string{root}, one.Identity.Canonical)
	if err != nil || got.Record.StoreKey != one.Record.StoreKey {
		t.Fatalf("exact source resolution=%+v err=%v", got, err)
	}
}

func TestLocalStoreReopensFromOriginalSourceNotDisplayNameOrDestination(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	root := filepath.Join(base, "state", "plugins")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := storeTestManifest()
	record, err := NewLocalInstallRecord(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeStoreTestPackage(t, root, record, manifest)
	identity, err := RuntimeIdentityForInstalledDir(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := RuntimeIdentityForLocalSource(manifest, source)
	if err != nil {
		t.Fatal(err)
	}
	if identity != want || strings.Contains(identity.Canonical, manifest.Name) || strings.Contains(identity.Canonical, filepath.Base(dir)) {
		t.Fatalf("local reopen identity=%+v want=%+v", identity, want)
	}
}

func TestInstalledStoreRejectsLegacyFlatAndSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	legacy := filepath.Join(root, "friendly-1.0.0")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := storeTestManifest()
	canonical, _ := manifest.Canonical()
	_ = os.WriteFile(filepath.Join(legacy, "plugin.manifest.json"), canonical, 0o600)
	_ = os.WriteFile(filepath.Join(legacy, "plugin.manifest.sig"), []byte("sig"), 0o600)
	if _, err := ListInstalledPackages(root); err == nil || !strings.Contains(err.Error(), "legacy flat") {
		t.Fatalf("legacy store accepted: %v", err)
	}
	if err := os.RemoveAll(legacy); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "local-"+strings.Repeat("0", 64))); err != nil {
		t.Fatal(err)
	}
	if _, err := ListInstalledPackages(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("installed symlink did not fail discovery closed: %v", err)
	}
}

func TestActivePackageMarkerIsNamespaceAndStoreKeyBound(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	manifest := storeTestManifest()
	pkg := addRemoteStoreTestPackage(t, root, "github.com/one/repo@v1.0.0", manifest)
	if err := WriteActivePackageMarker(root, pkg); err != nil {
		t.Fatal(err)
	}
	selected, ok, err := PickActivePackage(root, pkg.Identity.Namespace, []InstalledPackage{pkg})
	if err != nil || !ok || selected.Record.StoreKey != pkg.Record.StoreKey {
		t.Fatalf("selected=%+v ok=%t err=%v", selected, ok, err)
	}
}

func TestActivePackageMarkerCorruptionAndStalenessFailClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	manifest := storeTestManifest()
	pkg := addRemoteStoreTestPackage(t, root, "github.com/one/repo@v1.0.0", manifest)
	markerDir := filepath.Join(root, activeMarkerDir)
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(markerDir, activeNamespaceMarkerName(pkg.Identity.Namespace))
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "empty", data: nil, want: "invalid store key"},
		{name: "malformed", data: []byte("friendly-1.0.0"), want: "invalid store key"},
		{name: "oversized", data: []byte(strings.Repeat("x", int(maxActiveVersionMarkerBytes)+1)), want: "exceeds"},
		{name: "stale", data: []byte("remote-" + strings.Repeat("f", 64)), want: "missing store key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(markerPath, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := PickActivePackage(root, pkg.Identity.Namespace, []InstalledPackage{pkg}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("marker error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestCleanupDevRemovesAllExactSourceRebuildsAndMarker(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	root := filepath.Join(stateDir, "plugins")
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	var packages []InstalledPackage
	for _, wasmDigest := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		manifest := storeTestManifest()
		manifest.Version = DevSentinelVersion
		manifest.WASMSHA256 = wasmDigest
		record, err := NewLocalInstallRecord(source, manifest)
		if err != nil {
			t.Fatal(err)
		}
		dir := writeStoreTestPackage(t, root, record, manifest)
		identity, err := RuntimeIdentityForInstalledDir(dir, manifest)
		if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, InstalledPackage{Dir: dir, Record: record, Manifest: manifest, Identity: identity})
	}
	if err := WriteActivePackageMarker(root, packages[1]); err != nil {
		t.Fatal(err)
	}
	if err := CleanupDev(stateDir, source); err != nil {
		t.Fatal(err)
	}
	got, err := ListInstalledPackages(root)
	if err != nil || len(got) != 0 {
		t.Fatalf("remaining dev packages=%+v err=%v", got, err)
	}
	if marker, present, err := ReadActivePackageStoreKey(root, packages[0].Identity.Namespace); err != nil || present {
		t.Fatalf("dev marker survived cleanup: %s present=%v err=%v", marker, present, err)
	}
}

func TestAcceptedTagRewriteRetainsBothExactLockRowsAndRediscovery(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "plugins")
	manifest := storeTestManifest()
	id, err := ParseIdentity("github.com/acme/plugins/reviewer@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	commits := []string{storeTestCommit, "fedcba9876543210fedcba9876543210fedcba98"}
	lock := NewLock()
	var installed []InstalledPackage
	for _, commit := range commits {
		record, err := NewRemoteInstallRecord(id, id.Version, commit, manifest)
		if err != nil {
			t.Fatal(err)
		}
		dir := writeStoreTestPackage(t, root, record, manifest)
		entry, err := LockEntryFromResolvedManifest(id, id.Version, commit, manifest)
		if err != nil {
			t.Fatal(err)
		}
		lock.Add(entry)
		installed = append(installed, InstalledPackage{Dir: dir, Record: record, Manifest: manifest})
	}
	if len(lock.Entries) != 2 {
		t.Fatalf("tag rewrite overwrote old exact lock row: %+v", lock.Entries)
	}
	if err := lock.Write(filepath.Join(base, "plugin-lock.toml")); err != nil {
		t.Fatal(err)
	}
	packages, err := ListInstalledPackages(root)
	if err != nil || len(packages) != 2 {
		t.Fatalf("rewrite rediscovery packages=%+v err=%v", packages, err)
	}
	for i := range packages {
		installed[i].Identity = packages[i].Identity
	}
	newKey := installed[1].Record.StoreKey
	var replacement InstalledPackage
	for _, pkg := range packages {
		if pkg.Record.StoreKey == newKey {
			replacement = pkg
		}
	}
	if err := WriteActivePackageMarker(root, replacement); err != nil {
		t.Fatal(err)
	}
	selected, ok, err := PickActivePackage(root, replacement.Identity.Namespace, packages)
	if err != nil || !ok || selected.Record.StoreKey != newKey {
		t.Fatalf("accepted rewrite not selected: selected=%+v ok=%t err=%v", selected, ok, err)
	}
}
