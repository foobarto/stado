package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func writeTestLocalInstalledPackage(t *testing.T, pluginsRoot, source string, manifest plugins.Manifest, signature string, wasm []byte) plugins.InstalledPackage {
	t.Helper()
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pluginsRoot, record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"plugin.manifest.json": canonical,
		"plugin.manifest.sig":  []byte(signature),
		"plugin.wasm":          wasm,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.WriteInstallRecord(dir, record, manifest); err != nil {
		t.Fatal(err)
	}
	identity, err := plugins.RuntimeIdentityForInstalledDir(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return plugins.InstalledPackage{Dir: dir, Record: record, Manifest: manifest, Signature: signature, Identity: identity}
}
