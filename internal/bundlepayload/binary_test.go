package bundlepayload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestAppendToBinary_AndLoadFromFile: write a fake binary, append a
// bundle to it, then LoadFromFile and verify round-trip.
func TestAppendToBinary_AndLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "fake-stado")
	if err := os.WriteFile(src, []byte("fake go binary content"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	entries := []Entry{sampleEntry(t, "x")}

	dst := filepath.Join(dir, "bundled-stado")
	if err := AppendToBinary(src, dst, entries, bundlerPriv, bundlerPub); err != nil {
		t.Fatalf("AppendToBinary: %v", err)
	}

	bundle, err := LoadFromFile(dst, false)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if len(bundle.Entries) != 1 {
		t.Errorf("entry count = %d, want 1", len(bundle.Entries))
	}
	if !bytes.Equal(bundle.BundlerPubkey, bundlerPub) {
		t.Errorf("bundler pubkey roundtrip mismatch")
	}
}

// TestStripFromBinary: append a bundle, then strip — resulting file
// equals the source binary byte-for-byte.
func TestStripFromBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "fake-stado")
	srcBytes := []byte("fake go binary content for stripping")
	if err := os.WriteFile(src, srcBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	bundled := filepath.Join(dir, "bundled-stado")
	if err := AppendToBinary(src, bundled, []Entry{sampleEntry(t, "x")}, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}
	stripped := filepath.Join(dir, "stripped-stado")
	if err := StripFromBinary(bundled, stripped); err != nil {
		t.Fatalf("StripFromBinary: %v", err)
	}
	got, err := os.ReadFile(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, srcBytes) {
		t.Errorf("stripped output (%d bytes) does not match source (%d bytes)", len(got), len(srcBytes))
	}
}

// TestStripFromBinary_VanillaInput: stripping a binary that has no
// bundle is a no-op (output equals input).
func TestStripFromBinary_VanillaInput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "vanilla")
	srcBytes := []byte("vanilla bytes")
	if err := os.WriteFile(src, srcBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "stripped")
	if err := StripFromBinary(src, dst); err != nil {
		t.Fatalf("StripFromBinary on vanilla: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, srcBytes) {
		t.Error("strip on vanilla should produce identical output")
	}
}

// TestAppendToBinary_RefusesSamePath: src == dst is rejected.
func TestAppendToBinary_RefusesSamePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "same")
	_ = os.WriteFile(src, []byte("x"), 0o755)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := AppendToBinary(src, src, []Entry{sampleEntry(t, "y")}, priv, pub); err == nil {
		t.Error("expected error when src == dst; got nil")
	}
}
