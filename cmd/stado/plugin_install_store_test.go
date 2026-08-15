package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestActivateInstalledRecordSelectsChangedSameVersionLocalRebuild(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	root := filepath.Join(base, "plugins")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	makePackage := func(wasm string) plugins.InstalledPackage {
		sum := sha256.Sum256([]byte(wasm))
		manifest := plugins.Manifest{Name: "dev-alias", Version: "0.0.0-dev", WASMSHA256: hex.EncodeToString(sum[:])}
		record, err := plugins.NewLocalInstallRecord(source, manifest)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, record.StoreKey)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := activateInstalledRecord(root, dir, record, manifest); err != nil {
			t.Fatal(err)
		}
		identity, err := plugins.RuntimeIdentityForInstallRecord(root, record, manifest)
		if err != nil {
			t.Fatal(err)
		}
		return plugins.InstalledPackage{Dir: dir, Record: record, Manifest: manifest, Identity: identity}
	}
	oldPackage := makePackage("old-wasm")
	newPackage := makePackage("new-wasm")
	selected, ok, err := plugins.PickActivePackage(root, newPackage.Identity.Namespace, []plugins.InstalledPackage{oldPackage, newPackage})
	if err != nil || !ok {
		t.Fatalf("select active local rebuild: ok=%t err=%v", ok, err)
	}
	if selected.Record.StoreKey != newPackage.Record.StoreKey {
		t.Fatalf("same-version rebuild stayed on stale store key %s; want %s", selected.Record.StoreKey, newPackage.Record.StoreKey)
	}
}
