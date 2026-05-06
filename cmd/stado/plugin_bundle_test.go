package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/bundlepayload"
	"github.com/foobarto/stado/internal/plugins"
)

// sampleBundleEntry is a helper that constructs a properly-signed
// Entry for tests.
func sampleBundleEntry(t *testing.T) bundlepayload.Entry {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mf := plugins.Manifest{
		Name:    "stado-bundled-x",
		Version: "0.1.0",
		Author:  "test",
		Tools:   []plugins.ToolDef{{Name: "x_lookup", Description: "test"}},
	}
	canon, _ := mf.Canonical()
	wasm := []byte("\x00asm\x01\x00\x00\x00")
	sig := ed25519.Sign(priv, append(canon, wasm...))
	return bundlepayload.Entry{Pubkey: pub, Manifest: mf, Sig: sig, Wasm: wasm}
}

// TestStripRoundtrip: bundle a fixture binary → strip → resulting
// bytes match the original source.
func TestStripRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	srcBytes := []byte("vanilla source binary")
	if err := os.WriteFile(src, srcBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	bundled := filepath.Join(dir, "bundled")
	if err := bundlepayload.AppendToBinary(src, bundled, []bundlepayload.Entry{
		sampleBundleEntry(t),
	}, priv, pub); err != nil {
		t.Fatal(err)
	}
	stripped := filepath.Join(dir, "stripped")
	if err := bundlepayload.StripFromBinary(bundled, stripped); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(stripped)
	want, _ := os.ReadFile(src)
	if !bytes.Equal(got, want) {
		t.Errorf("strip roundtrip mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// TestLoadBundleFile_Roundtrip: write a small bundle.toml, load it,
// verify the parsed shape.
func TestLoadBundleFile_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.toml")
	content := `output = "stado-custom"
allow_unsigned = false

[[plugin]]
name = "htb-lab"

[[plugin]]
name = "gtfobins"
version = "0.1.0"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	bf, err := loadBundleFile(path)
	if err != nil {
		t.Fatalf("loadBundleFile: %v", err)
	}
	if bf.Output != "stado-custom" {
		t.Errorf("Output = %q, want stado-custom", bf.Output)
	}
	if len(bf.Plugins) != 2 {
		t.Fatalf("Plugins count = %d, want 2", len(bf.Plugins))
	}
	if !strings.EqualFold(bf.Plugins[1].Version, "0.1.0") {
		t.Errorf("Plugin[1].Version = %q, want 0.1.0", bf.Plugins[1].Version)
	}
}
