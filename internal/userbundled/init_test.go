package userbundled

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/bundlepayload"
	"github.com/foobarto/stado/internal/plugins"
)

// makeEntry builds a properly signed bundlepayload.Entry for testing.
func makeEntry(t *testing.T, bareName, toolName string, caps []string) bundlepayload.Entry {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wasm := []byte("\x00asm\x01\x00\x00\x00") // minimal valid wasm magic
	wasmHash := sha256.Sum256(wasm)
	mf := plugins.Manifest{
		Name:         bundledplugins.ManifestNamePrefix + "-" + bareName,
		Version:      "0.1.0",
		Author:       "test",
		Capabilities: caps,
		Tools:        []plugins.ToolDef{{Name: toolName, Description: "test tool"}},
		WASMSHA256:   hex.EncodeToString(wasmHash[:]),
	}
	canon, err := mf.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := ed25519.Sign(priv, canon)
	return bundlepayload.Entry{
		Pubkey:   pub,
		Manifest: mf,
		Sig:      sig,
		Wasm:     wasm,
	}
}

// TestLoadAndRegister_HappyPath: a binary with a valid bundle appended
// causes loadAndRegister to register tools in bundledplugins.
func TestLoadAndRegister_HappyPath(t *testing.T) {
	bundledplugins.ResetForTest(t)

	bundlerPub, bundlerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	entry := makeEntry(t, "myplugin", "myplugin__do", []string{"fs:read:."})

	// Write a fake binary (just a small sentinel header + bundle).
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "stado")
	if err := os.WriteFile(fakeBin, []byte("fake binary header"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bundlepayload.AppendToBinary(fakeBin, fakeBin+".bundled", []bundlepayload.Entry{entry}, bundlerPriv, bundlerPub); err != nil {
		t.Fatalf("AppendToBinary: %v", err)
	}

	if err := loadAndRegister(fakeBin+".bundled", false); err != nil {
		t.Fatalf("loadAndRegister: %v", err)
	}

	list := bundledplugins.List()
	found := false
	for _, info := range list {
		if info.Name == "myplugin" {
			found = true
			if len(info.WasmSource) == 0 {
				t.Error("WasmSource should be non-empty after RegisterModuleWithWasm")
			}
			toolFound := false
			for _, tn := range info.Tools {
				if tn == "myplugin__do" {
					toolFound = true
				}
			}
			if !toolFound {
				t.Errorf("expected tool 'myplugin__do' in Tools; got %v", info.Tools)
			}
			_ = bundlerPub // Bundler package-var is set; spot-check via SkipVerifyApplied
			if SkipVerifyApplied {
				t.Error("SkipVerifyApplied should be false for a fully-verified load")
			}
		}
	}
	if !found {
		t.Errorf("'myplugin' not found in bundledplugins.List(); got %+v", list)
	}
}

// TestLoadAndRegister_VanillaIsNoOp: a binary with no bundle payload
// produces no error and no registrations.
func TestLoadAndRegister_VanillaIsNoOp(t *testing.T) {
	bundledplugins.ResetForTest(t)

	dir := t.TempDir()
	vanillaBin := filepath.Join(dir, "stado-vanilla")
	if err := os.WriteFile(vanillaBin, []byte("just a go binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := loadAndRegister(vanillaBin, false); err != nil {
		t.Fatalf("loadAndRegister on vanilla binary: %v", err)
	}

	if got := bundledplugins.List(); len(got) != 0 {
		t.Errorf("expected empty list for vanilla binary; got %+v", got)
	}
}
