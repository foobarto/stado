package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
)

// buildTestPluginWithCaps mirrors buildTestPlugin in plugin_install_test.go
// but allows declaring extra capabilities (and a stub tool that matches the
// capability so the manifest is internally consistent). Used by plugin_gc_test
// and other plugin-flow tests that need a plugin with non-default caps.
func buildTestPluginWithCaps(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, name, version string, caps []string) string {
	t.Helper()
	dir := t.TempDir()

	wasm := []byte("pretend-wasm-blob-" + name)
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	m := &plugins.Manifest{
		Name:            name,
		Version:         version,
		Author:          "test-author",
		AuthorPubkeyFpr: plugins.Fingerprint(pub),
		WASMSHA256:      hex.EncodeToString(sum[:]),
		Capabilities:    caps,
		Tools: []plugins.ToolDef{{
			Name:        "anything",
			Description: "test stub",
			Schema:      `{"type":"object"}`,
		}},
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
	}
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
