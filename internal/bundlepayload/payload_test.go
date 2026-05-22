package bundlepayload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func sampleEntry(t *testing.T, name string) Entry {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wasm := []byte("\x00asm\x01\x00\x00\x00")
	wasmHash := sha256.Sum256(wasm)
	mf := plugins.Manifest{
		Name:       "stado-bundled-" + name,
		Version:    "0.1.0",
		Author:     "test",
		Tools:      []plugins.ToolDef{{Name: name + "_lookup", Description: "test"}},
		WASMSHA256: hex.EncodeToString(wasmHash[:]),
	}
	canon, err := mf.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := ed25519.Sign(priv, canon)
	return Entry{
		Pubkey:   pub,
		Manifest: mf,
		Sig:      sig,
		Wasm:     wasm,
	}
}

// entryWithDigest builds a validly-signed entry whose manifest
// WASMSHA256 is set to the given value (which may be empty or
// malformed). The author signature covers the canonical manifest as
// authored, so the entry passes the per-entry *signature* check —
// exercising #026's separate digest-binding gate.
func entryWithDigest(t *testing.T, name, digest string) Entry {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wasm := []byte("\x00asm\x01\x00\x00\x00")
	mf := plugins.Manifest{
		Name:       "stado-bundled-" + name,
		Version:    "0.1.0",
		Author:     "test",
		Tools:      []plugins.ToolDef{{Name: name + "_lookup", Description: "test"}},
		WASMSHA256: digest,
	}
	canon, err := mf.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	return Entry{Pubkey: pub, Manifest: mf, Sig: ed25519.Sign(priv, canon), Wasm: wasm}
}

// TestDecode_EmptyWASMDigestRejected: #026 — an entry whose manifest
// has an empty wasm_sha256 has no cryptographic binding to the wasm
// bytes (the author sig covers only the canonical manifest). Before
// the fix the `!= ""` guard let it pass; now it must be rejected.
func TestDecode_EmptyWASMDigestRejected(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	var buf bytes.Buffer
	if err := Encode(&buf, []Entry{entryWithDigest(t, "empty", "")}, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeBytes(buf.Bytes(), false)
	if err == nil {
		t.Fatal("expected rejection of empty wasm_sha256; got nil")
	}
	if !errors.Is(err, ErrEntrySigInvalid) {
		t.Errorf("error = %v, want ErrEntrySigInvalid", err)
	}
}

// TestDecode_MalformedWASMDigestRejected: #026 — a non-hex / wrong-
// length wasm_sha256 is also rejected before the comparison.
func TestDecode_MalformedWASMDigestRejected(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	for _, bad := range []string{"not-hex", "abc", "ZZZZ"} {
		var buf bytes.Buffer
		if err := Encode(&buf, []Entry{entryWithDigest(t, "mal", bad)}, bundlerPriv, bundlerPub); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeBytes(buf.Bytes(), false); !errors.Is(err, ErrEntrySigInvalid) {
			t.Errorf("digest %q: error = %v, want ErrEntrySigInvalid", bad, err)
		}
	}
}

// TestDecode_WrongWASMDigestRejected: #026 — a well-formed digest
// that doesn't match sha256(wasm) is rejected (regression guard for
// the existing mismatch path, now unconditional).
func TestDecode_WrongWASMDigestRejected(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	wrong := hex.EncodeToString(sha256.New().Sum(nil)) // sha256 of empty, not of the wasm
	var buf bytes.Buffer
	if err := Encode(&buf, []Entry{entryWithDigest(t, "wrong", wrong)}, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBytes(buf.Bytes(), false); !errors.Is(err, ErrEntrySigInvalid) {
		t.Errorf("error = %v, want ErrEntrySigInvalid", err)
	}
}

// TestEncodeDecode_RoundTrip: encoding then decoding produces
// identical entries + verified bundler sig.
func TestEncodeDecode_RoundTrip(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	entries := []Entry{sampleEntry(t, "alpha"), sampleEntry(t, "beta")}

	var buf bytes.Buffer
	if err := Encode(&buf, entries, bundlerPriv, bundlerPub); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	bundle, err := DecodeBytes(buf.Bytes(), false)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	if !bytes.Equal(bundle.BundlerPubkey, bundlerPub) {
		t.Errorf("bundler pubkey roundtrip mismatch")
	}
	if len(bundle.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(bundle.Entries))
	}
	for i, want := range entries {
		got := bundle.Entries[i]
		if got.Manifest.Name != want.Manifest.Name {
			t.Errorf("entry %d name = %q, want %q", i, got.Manifest.Name, want.Manifest.Name)
		}
		if !bytes.Equal(got.Wasm, want.Wasm) {
			t.Errorf("entry %d wasm mismatch", i)
		}
	}
}

// TestDecode_NoMagic: input that does not end with STADO_BUNDLE_END
// returns (zero, nil) — vanilla binary, not an error.
func TestDecode_NoMagic(t *testing.T) {
	got, err := DecodeBytes([]byte("just some go binary bytes"), false)
	if err != nil {
		t.Errorf("expected nil error for vanilla input; got %v", err)
	}
	if len(got.Entries) != 0 || got.BundlerPubkey != nil {
		t.Errorf("expected empty bundle for vanilla input; got %+v", got)
	}
}

// TestDecode_BundlerSigInvalid: tampering with payload-body fails
// the bundler sig check.
func TestDecode_BundlerSigInvalid(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	entries := []Entry{sampleEntry(t, "alpha")}

	var buf bytes.Buffer
	if err := Encode(&buf, entries, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	// Flip a byte deep in the payload-body (after the leading magic,
	// well before the trailer). Index 32 lands inside the entry-count
	// or first entry.
	raw[32] ^= 0x01

	_, err := DecodeBytes(raw, false)
	if err == nil {
		t.Fatal("expected ErrBundlerSigInvalid; got nil")
	}
}

// TestDecode_PerEntrySigInvalid: tampering with one entry's wasm
// passes bundler sig check (since we re-sign the bundle here)
// but fails per-entry sig check.
func TestDecode_PerEntrySigInvalid(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	bad := sampleEntry(t, "bad")
	bad.Wasm = []byte("different bytes — sig won't match")

	var buf bytes.Buffer
	if err := Encode(&buf, []Entry{bad}, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}

	_, err := DecodeBytes(buf.Bytes(), false)
	if err == nil {
		t.Fatal("expected per-entry sig failure; got nil")
	}
}

// TestDecode_SkipVerify: even with corruption, skipVerify=true
// returns the entries (signature checks bypassed).
func TestDecode_SkipVerify(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	entries := []Entry{sampleEntry(t, "alpha")}

	var buf bytes.Buffer
	if err := Encode(&buf, entries, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[32] ^= 0x01 // tamper the payload-body

	bundle, err := DecodeBytes(raw, true)
	if err != nil {
		t.Fatalf("skip-verify should succeed despite tamper; got %v", err)
	}
	if !bundle.SkipVerified {
		t.Error("SkipVerified should be true when skipVerify was honoured")
	}
	if len(bundle.Entries) != 1 {
		t.Errorf("expected 1 entry; got %d", len(bundle.Entries))
	}
}

// TestDecode_StructurallyBroken: a truncated payload errors even
// with skipVerify=true (structural validation always runs).
func TestDecode_StructurallyBroken(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	var buf bytes.Buffer
	if err := Encode(&buf, []Entry{sampleEntry(t, "alpha")}, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}
	// Keep just enough trailing bytes so the minimum-size gate passes
	// (trailerLen + magicStart + PublicKeySize + SignatureSize = 136)
	// but the embedded trailer-size value points outside this window,
	// making the parse error on structural grounds.
	raw := buf.Bytes()
	const minSize = (8 + len(magicEnd)) + len(magicStart) + ed25519.PublicKeySize + ed25519.SignatureSize
	truncated := raw[len(raw)-minSize:]

	if _, err := DecodeBytes(truncated, true); err == nil {
		t.Error("expected structural error on truncated payload even with skipVerify")
	}
}

// TestEncode_Deterministic: encoding the same input twice with the
// same bundler key produces byte-identical output. Required for
// reproducible-build use cases.
func TestEncode_Deterministic(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	entries := []Entry{sampleEntry(t, "alpha"), sampleEntry(t, "beta")}

	var b1, b2 bytes.Buffer
	_ = Encode(&b1, entries, priv, pub)
	_ = Encode(&b2, entries, priv, pub)
	if !bytes.Equal(b1.Bytes(), b2.Bytes()) {
		t.Errorf("encode is not deterministic: lengths %d vs %d", b1.Len(), b2.Len())
	}
}

// jsonRoundtrip is a guard test that confirms plugins.Manifest
// survives JSON round-trip cleanly inside our entry format. If
// it ever doesn't, the encoder needs a custom marshaller.
func TestEntryManifestRoundtrip(t *testing.T) {
	mf := sampleEntry(t, "x").Manifest
	data, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	var got plugins.Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != mf.Name || got.Version != mf.Version {
		t.Errorf("manifest roundtrip lost name/version")
	}
}
