# Plugin Bundle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `stado plugin bundle <ids>... --out=<binary>` (plus `--strip` and `--info` modes) — appends already-compiled wasm plugins to the trailing bytes of a stado binary, with two-level signature verification (per-entry author sigs + outer bundler sig) and a `--unsafe-skip-bundle-verify` runtime escape hatch. Produces a portable, self-contained stado tailored to the operator's plugin selection — without requiring a Go toolchain.

**Architecture:** A new `internal/bundlepayload/` package owns the on-disk payload format (length-prefixed entries + outer Ed25519 signature + trailing magic for self-discovery). A new `internal/userbundled/` package runs at `init()` and threads verified bundles through the existing `bundledplugins.RegisterModule` path — making user-bundled plugins indistinguishable from upstream-shipped bundled plugins at runtime. The `bundle` cobra subcommand handles bundle/strip/info actions; reuses `runtime.ResolveInstalledPluginDir` (just landed) for bare-name resolution.

**Tech Stack:** Go 1.22+, cobra, `crypto/ed25519`, `crypto/sha256`, the existing `internal/plugins` (trust store, Manifest.Canonical, Fingerprint) + `internal/bundledplugins` packages. No new external deps.

---

### Task 1: `internal/bundlepayload/payload.go` — encode + parse (TDD)

**Files:**
- Create: `internal/bundlepayload/payload.go`
- Create: `internal/bundlepayload/payload_test.go`

This task implements the wire format. No file I/O yet — all functions take `[]byte` or `io.Reader`/`io.Writer`. The next task wires it to `os.Executable()`.

**Wire format (from spec):**
```
<STADO_BUNDLE_v1 magic, 16B>
<payload-body>             ← entry-count + entries
<bundler-pubkey, 32B>
<bundler-sig, 64B>
<trailer-size uint64 LE, 8B>   ← size of (payload-body + bundler-pubkey + bundler-sig)
<STADO_BUNDLE_END magic, 16B>
```

Per-entry format:
```
<pubkey-len uint16 LE, 2B> <pubkey>
<manifest-len uint32 LE, 4B> <manifest-json>
<sig-len uint16 LE, 2B> <sig>
<wasm-len uint32 LE, 4B> <wasm>
```

- [ ] **Step 1: Write the failing tests**

Create `internal/bundlepayload/payload_test.go`:

```go
package bundlepayload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func sampleEntry(t *testing.T, name string) Entry {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mf := plugins.Manifest{
		Name:    "stado-bundled-" + name,
		Version: "0.1.0",
		Author:  "test",
		Tools:   []plugins.ToolDef{{Name: name + "_lookup", Description: "test"}},
	}
	canon, err := mf.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	wasm := []byte("\x00asm\x01\x00\x00\x00")
	sig := ed25519.Sign(priv, append(canon, wasm...))
	return Entry{
		Pubkey:   pub,
		Manifest: mf,
		Sig:      sig,
		Wasm:     wasm,
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
	// Truncate to a length that includes the trailing magic but
	// makes the size field point outside the buffer.
	truncated := buf.Bytes()[:30]

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bundlepayload/ -count=1 -v`
Expected: FAIL with "undefined: Encode / DecodeBytes / Entry / Bundle".

- [ ] **Step 3: Write the implementation**

Create `internal/bundlepayload/payload.go`:

```go
// Package bundlepayload implements the on-disk format for plugins
// appended to a stado binary by `stado plugin bundle`.
//
// Payload layout (appended to a vanilla stado binary):
//
//   <STADO_BUNDLE_v1 magic, 16 bytes>
//   <payload-body>
//   <bundler-pubkey, 32 bytes>      ← Ed25519 pubkey of the bundling identity
//   <bundler-sig, 64 bytes>         ← Ed25519 sig over sha256(payload-body)
//   <trailer-size uint64 LE, 8B>    ← size of (payload-body+pubkey+sig)
//   <STADO_BUNDLE_END magic, 16 bytes>
//
// Per-entry inside payload-body:
//   <count uint32 LE> { <pubkey-len uint16> <pubkey>
//                       <manifest-len uint32> <manifest-json>
//                       <sig-len uint16> <sig>
//                       <wasm-len uint32> <wasm> } * count
//
// Sorting + deterministic encoding: entries are sorted by manifest
// Name before encoding so byte-for-byte reproducibility holds for
// equivalent inputs.
package bundlepayload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/foobarto/stado/internal/plugins"
)

const (
	magicStart = "STADO_BUNDLE_v1\n"
	magicEnd   = "STADO_BUNDLE_END"
)

var (
	ErrCorrupt              = errors.New("bundlepayload: payload structurally corrupt")
	ErrBundlerSigInvalid    = errors.New("bundlepayload: bundler signature invalid")
	ErrEntrySigInvalid      = errors.New("bundlepayload: per-entry signature invalid")
)

// Entry is one user-bundled plugin: its verifying pubkey, manifest,
// signature, and wasm bytes.
type Entry struct {
	Pubkey   ed25519.PublicKey
	Manifest plugins.Manifest
	Sig      []byte
	Wasm     []byte
}

// Bundle is the parsed payload: bundler identity + verified entries.
type Bundle struct {
	BundlerPubkey ed25519.PublicKey
	Entries       []Entry
	SkipVerified  bool // true when DecodeBytes was called with skipVerify=true
}

// Encode writes the full appended payload (magic + body + bundler
// signature + trailer + end magic) to w. Entries are sorted by
// manifest Name for deterministic output.
func Encode(w io.Writer, entries []Entry, bundlerPriv ed25519.PrivateKey, bundlerPub ed25519.PublicKey) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Manifest.Name < sorted[j].Manifest.Name
	})

	var body bytes.Buffer
	if err := binary.Write(&body, binary.LittleEndian, uint32(len(sorted))); err != nil {
		return err
	}
	for _, e := range sorted {
		if err := writeEntry(&body, e); err != nil {
			return err
		}
	}

	digest := sha256.Sum256(body.Bytes())
	bundlerSig := ed25519.Sign(bundlerPriv, digest[:])

	if _, err := io.WriteString(w, magicStart); err != nil {
		return err
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		return err
	}
	if _, err := w.Write(bundlerPub); err != nil {
		return err
	}
	if _, err := w.Write(bundlerSig); err != nil {
		return err
	}
	trailerSize := uint64(body.Len() + ed25519.PublicKeySize + ed25519.SignatureSize)
	if err := binary.Write(w, binary.LittleEndian, trailerSize); err != nil {
		return err
	}
	if _, err := io.WriteString(w, magicEnd); err != nil {
		return err
	}
	return nil
}

// writeEntry writes one entry's length-prefixed fields.
func writeEntry(w io.Writer, e Entry) error {
	manifestJSON, err := json.Marshal(e.Manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeLenPrefixedU16(w, e.Pubkey); err != nil {
		return err
	}
	if err := writeLenPrefixedU32(w, manifestJSON); err != nil {
		return err
	}
	if err := writeLenPrefixedU16(w, e.Sig); err != nil {
		return err
	}
	return writeLenPrefixedU32(w, e.Wasm)
}

func writeLenPrefixedU16(w io.Writer, b []byte) error {
	if len(b) > 0xFFFF {
		return fmt.Errorf("field too large for uint16 length: %d", len(b))
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func writeLenPrefixedU32(w io.Writer, b []byte) error {
	if uint64(len(b)) > 0xFFFFFFFF {
		return fmt.Errorf("field too large for uint32 length: %d", len(b))
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// DecodeBytes parses raw bytes (e.g. the tail of a binary file) and
// returns the verified Bundle. Returns (zero, nil) when no bundle
// magic is present (vanilla input). Returns an error when a bundle
// IS present but malformed or signature-invalid (unless skipVerify
// is true, in which case signature checks are bypassed but
// structural validation still applies).
func DecodeBytes(raw []byte, skipVerify bool) (Bundle, error) {
	const trailerLen = 8 + len(magicEnd)
	if len(raw) < trailerLen+len(magicStart)+ed25519.PublicKeySize+ed25519.SignatureSize {
		// Too short to possibly contain a bundle; vanilla.
		return Bundle{}, nil
	}
	if string(raw[len(raw)-len(magicEnd):]) != magicEnd {
		return Bundle{}, nil // vanilla
	}
	sizeOff := len(raw) - len(magicEnd) - 8
	trailerSize := binary.LittleEndian.Uint64(raw[sizeOff : sizeOff+8])
	if trailerSize > uint64(len(raw)) {
		return Bundle{}, fmt.Errorf("%w: trailer size out of range", ErrCorrupt)
	}
	startOff := uint64(sizeOff) - trailerSize - uint64(len(magicStart))
	if int64(startOff) < 0 || startOff > uint64(len(raw)) {
		return Bundle{}, fmt.Errorf("%w: start offset out of range", ErrCorrupt)
	}
	if string(raw[startOff:startOff+uint64(len(magicStart))]) != magicStart {
		return Bundle{}, fmt.Errorf("%w: leading magic missing", ErrCorrupt)
	}
	bodyAndKeys := raw[startOff+uint64(len(magicStart)) : sizeOff]
	if len(bodyAndKeys) < ed25519.PublicKeySize+ed25519.SignatureSize {
		return Bundle{}, fmt.Errorf("%w: trailer too small for pubkey+sig", ErrCorrupt)
	}
	bodyEnd := len(bodyAndKeys) - ed25519.PublicKeySize - ed25519.SignatureSize
	body := bodyAndKeys[:bodyEnd]
	bundlerPub := ed25519.PublicKey(bodyAndKeys[bodyEnd : bodyEnd+ed25519.PublicKeySize])
	bundlerSig := bodyAndKeys[bodyEnd+ed25519.PublicKeySize:]

	if !skipVerify {
		digest := sha256.Sum256(body)
		if !ed25519.Verify(bundlerPub, digest[:], bundlerSig) {
			return Bundle{}, ErrBundlerSigInvalid
		}
	}

	entries, err := decodeEntries(body)
	if err != nil {
		return Bundle{}, err
	}

	if !skipVerify {
		for i, e := range entries {
			canon, err := e.Manifest.Canonical()
			if err != nil {
				return Bundle{}, fmt.Errorf("entry %d: canonicalize: %w", i, err)
			}
			if !ed25519.Verify(e.Pubkey, append(canon, e.Wasm...), e.Sig) {
				return Bundle{}, fmt.Errorf("entry %d (%s): %w", i, e.Manifest.Name, ErrEntrySigInvalid)
			}
		}
	}

	return Bundle{
		BundlerPubkey: bundlerPub,
		Entries:       entries,
		SkipVerified:  skipVerify,
	}, nil
}

func decodeEntries(body []byte) ([]Entry, error) {
	r := bytes.NewReader(body)
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("%w: entry count", ErrCorrupt)
	}
	entries := make([]Entry, 0, count)
	for i := uint32(0); i < count; i++ {
		e, err := readEntry(r)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func readEntry(r *bytes.Reader) (Entry, error) {
	pub, err := readLenPrefixedU16(r)
	if err != nil {
		return Entry{}, err
	}
	if len(pub) != ed25519.PublicKeySize {
		return Entry{}, fmt.Errorf("%w: pubkey length %d", ErrCorrupt, len(pub))
	}
	manifestBytes, err := readLenPrefixedU32(r)
	if err != nil {
		return Entry{}, err
	}
	var mf plugins.Manifest
	if err := json.Unmarshal(manifestBytes, &mf); err != nil {
		return Entry{}, fmt.Errorf("%w: manifest json: %v", ErrCorrupt, err)
	}
	sig, err := readLenPrefixedU16(r)
	if err != nil {
		return Entry{}, err
	}
	if len(sig) != ed25519.SignatureSize {
		return Entry{}, fmt.Errorf("%w: sig length %d", ErrCorrupt, len(sig))
	}
	wasm, err := readLenPrefixedU32(r)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Pubkey: ed25519.PublicKey(pub), Manifest: mf, Sig: sig, Wasm: wasm}, nil
}

func readLenPrefixedU16(r *bytes.Reader) ([]byte, error) {
	var n uint16
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("%w: u16 length", ErrCorrupt)
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("%w: u16 body", ErrCorrupt)
	}
	return out, nil
}

func readLenPrefixedU32(r *bytes.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("%w: u32 length", ErrCorrupt)
	}
	if uint64(n) > uint64(r.Len()+1) {
		return nil, fmt.Errorf("%w: u32 length too large", ErrCorrupt)
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("%w: u32 body", ErrCorrupt)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bundlepayload/ -count=1 -v`
Expected: PASS for all 7 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/bundlepayload/payload.go internal/bundlepayload/payload_test.go
git commit -m "feat(bundlepayload): two-level signed payload encoder + decoder"
```

---

### Task 2: `internal/bundlepayload/binary.go` — read from / write to a binary file

**Files:**
- Create: `internal/bundlepayload/binary.go`
- Create: `internal/bundlepayload/binary_test.go`

This task wraps Encode/DecodeBytes for the `os.Executable()` case + the strip case.

- [ ] **Step 1: Write the failing tests**

Create `internal/bundlepayload/binary_test.go`:

```go
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
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/bundlepayload/ -run "TestAppendToBinary|TestStripFromBinary" -count=1 -v`
Expected: FAIL with "undefined: AppendToBinary / LoadFromFile / StripFromBinary".

- [ ] **Step 3: Write the implementation**

Create `internal/bundlepayload/binary.go`:

```go
package bundlepayload

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AppendToBinary copies srcPath to dstPath (preserving execute
// permission), then appends a signed bundle. Refuses when srcPath
// resolves to dstPath.
func AppendToBinary(srcPath, dstPath string, entries []Entry, bundlerPriv ed25519.PrivateKey, bundlerPub ed25519.PublicKey) error {
	srcAbs, err := filepath.Abs(srcPath)
	if err != nil {
		return err
	}
	dstAbs, err := filepath.Abs(dstPath)
	if err != nil {
		return err
	}
	if srcAbs == dstAbs {
		return errors.New("bundlepayload: source and destination paths are the same; refusing to overwrite running binary")
	}

	if err := copyFile(srcAbs, dstAbs); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	f, err := os.OpenFile(dstAbs, os.O_WRONLY|os.O_APPEND, 0o755)
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	defer f.Close()

	return Encode(f, entries, bundlerPriv, bundlerPub)
}

// LoadFromFile opens path, reads only the trailing tail needed to
// resolve the bundle (cheap for very large binaries), and returns
// the parsed Bundle. Returns (zero, nil) for vanilla binaries.
func LoadFromFile(path string, skipVerify bool) (Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return Bundle{}, err
	}
	const trailerLen = 8 + 16 // size field + STADO_BUNDLE_END magic
	if st.Size() < int64(trailerLen) {
		return Bundle{}, nil
	}
	tail := make([]byte, trailerLen)
	if _, err := f.ReadAt(tail, st.Size()-int64(trailerLen)); err != nil {
		return Bundle{}, err
	}
	if string(tail[8:]) != magicEnd {
		return Bundle{}, nil // vanilla
	}
	trailerSize := binary.LittleEndian.Uint64(tail[:8])
	totalBundleLen := int64(trailerSize) + int64(len(magicStart)) + int64(trailerLen)
	if totalBundleLen > st.Size() {
		return Bundle{}, fmt.Errorf("%w: bundle larger than file", ErrCorrupt)
	}
	bundleBytes := make([]byte, totalBundleLen)
	if _, err := f.ReadAt(bundleBytes, st.Size()-totalBundleLen); err != nil {
		return Bundle{}, err
	}
	return DecodeBytes(bundleBytes, skipVerify)
}

// StripFromBinary copies srcPath to dstPath, truncated at the
// bundle's start magic. If srcPath has no bundle, dst is a byte-
// identical copy. Refuses when src == dst.
func StripFromBinary(srcPath, dstPath string) error {
	srcAbs, _ := filepath.Abs(srcPath)
	dstAbs, _ := filepath.Abs(dstPath)
	if srcAbs == dstAbs {
		return errors.New("bundlepayload: source and destination paths are the same")
	}
	src, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer src.Close()

	st, err := src.Stat()
	if err != nil {
		return err
	}

	stripAt := st.Size()
	const trailerLen = 8 + 16
	if st.Size() >= int64(trailerLen) {
		tail := make([]byte, trailerLen)
		if _, err := src.ReadAt(tail, st.Size()-int64(trailerLen)); err == nil {
			if string(tail[8:]) == magicEnd {
				trailerSize := binary.LittleEndian.Uint64(tail[:8])
				stripAt = st.Size() - int64(trailerSize) - int64(len(magicStart)) - int64(trailerLen)
				if stripAt < 0 {
					return fmt.Errorf("%w: strip offset negative", ErrCorrupt)
				}
			}
		}
	}

	dst, err := os.OpenFile(dstAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.CopyN(dst, src, stripAt); err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bundlepayload/ -count=1 -v`
Expected: PASS for all 11 tests (7 from Task 1 + 4 from Task 2).

- [ ] **Step 5: Commit**

```bash
git add internal/bundlepayload/binary.go internal/bundlepayload/binary_test.go
git commit -m "feat(bundlepayload): file-level Append/Load/Strip helpers"
```

---

### Task 3: `bundledplugins.Info.WasmSource` field + `Wasm()` consultation

**Files:**
- Modify: `internal/bundledplugins/list.go`
- Modify: `internal/bundledplugins/wasm.go`

This task lets user-bundled plugins (which carry raw bytes) flow through the same `bundledplugins.Wasm(name)` lookup that upstream-shipped plugins use (which carry an embed.FS path).

- [ ] **Step 1: Read the existing Info struct + Wasm function**

Run: `grep -n "type Info\|func Wasm" internal/bundledplugins/*.go`

Locate the Info struct definition and the Wasm function.

- [ ] **Step 2: Add the WasmSource field**

In `internal/bundledplugins/list.go`'s `Info` struct, add:

```go
// WasmSource carries raw wasm bytes for user-bundled plugins
// (registered via internal/userbundled). When non-nil, Wasm()
// returns these bytes directly instead of consulting the embed.FS.
// nil for upstream-shipped bundled plugins.
WasmSource []byte
```

- [ ] **Step 3: Modify `Wasm(name string) ([]byte, error)`**

In `internal/bundledplugins/wasm.go` (or wherever `Wasm` is defined), at the top of the function — after looking up the Info — add:

```go
if info.WasmSource != nil {
    return info.WasmSource, nil
}
// ... existing embed.FS lookup path ...
```

Make sure the Info lookup happens FIRST so the embed.FS path can fall through cleanly when WasmSource is nil.

- [ ] **Step 4: Verify build + existing tests still pass**

Run: `go build ./... && go test ./internal/bundledplugins/ -count=1`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/bundledplugins/list.go internal/bundledplugins/wasm.go
git commit -m "feat(bundledplugins): WasmSource field for user-bundled raw bytes"
```

---

### Task 4: `internal/userbundled/init.go` — runtime registration

**Files:**
- Create: `internal/userbundled/init.go`
- Create: `internal/userbundled/init_test.go`

The package's init() runs at startup, calls `bundlepayload.LoadFromFile(os.Executable(), skipVerify)`, and threads each verified entry through `bundledplugins.RegisterModule`. Exposes the verified bundler pubkey + skip-verify state for `--version` consumers.

- [ ] **Step 1: Write a test that exercises the registration path**

Create `internal/userbundled/init_test.go`:

```go
package userbundled

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/bundlepayload"
	"github.com/foobarto/stado/internal/plugins"
)

// TestLoadAndRegister_HappyPath: build a fake binary with a bundle,
// call loadAndRegister(path) directly, verify the bundled plugin
// shows up in bundledplugins.List().
func TestLoadAndRegister_HappyPath(t *testing.T) {
	bundlerPub, bundlerPriv, _ := ed25519.GenerateKey(rand.Reader)
	authorPub, authorPriv, _ := ed25519.GenerateKey(rand.Reader)

	mf := plugins.Manifest{
		Name:    "stado-bundled-testpkg",
		Version: "0.1.0",
		Author:  "test",
		Tools:   []plugins.ToolDef{{Name: "testpkg_lookup", Description: "test"}},
	}
	canon, _ := mf.Canonical()
	wasm := []byte("\x00asm\x01\x00\x00\x00")
	sig := ed25519.Sign(authorPriv, append(canon, wasm...))

	dir := t.TempDir()
	src := filepath.Join(dir, "fake")
	_ = os.WriteFile(src, []byte("fake binary"), 0o755)
	dst := filepath.Join(dir, "bundled")
	if err := bundlepayload.AppendToBinary(src, dst, []bundlepayload.Entry{{
		Pubkey: authorPub, Manifest: mf, Sig: sig, Wasm: wasm,
	}}, bundlerPriv, bundlerPub); err != nil {
		t.Fatal(err)
	}

	// Reset bundledplugins registry-state for this test so prior
	// registrations don't leak. (Implementation note: package-level
	// registry must expose a Reset() helper for tests.)
	bundledplugins.ResetForTest()

	if err := loadAndRegister(dst, false); err != nil {
		t.Fatalf("loadAndRegister: %v", err)
	}

	infos := bundledplugins.List()
	found := false
	for _, info := range infos {
		if info.Name == "testpkg" {
			found = true
			if string(info.WasmSource) != string(wasm) {
				t.Errorf("WasmSource mismatch")
			}
		}
	}
	if !found {
		t.Errorf("testpkg not registered; got: %+v", infos)
	}
}

// TestLoadAndRegister_VanillaIsNoOp: a binary with no bundle → no
// error, no registrations.
func TestLoadAndRegister_VanillaIsNoOp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "vanilla")
	_ = os.WriteFile(src, []byte("vanilla content"), 0o755)
	bundledplugins.ResetForTest()
	if err := loadAndRegister(src, false); err != nil {
		t.Errorf("loadAndRegister on vanilla: %v", err)
	}
}
```

- [ ] **Step 2: Add `bundledplugins.ResetForTest()` helper**

In `internal/bundledplugins/list.go`, append:

```go
// ResetForTest clears the in-memory registry. Used by tests in
// internal/userbundled and elsewhere that need a clean slate.
// Not exposed as Reset() to discourage non-test use.
func ResetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}
```

(Adjust `registryMu` / `registry` to the actual variable names in
the file. The existing `RegisterModule` writes to that state — read
its body to confirm names.)

- [ ] **Step 3: Verify the test fails on the right symbol**

Run: `go test ./internal/userbundled/ -count=1 -v`
Expected: FAIL with "undefined: loadAndRegister".

- [ ] **Step 4: Write the implementation**

Create `internal/userbundled/init.go`:

```go
// Package userbundled registers user-bundled plugins (appended to
// the stado binary by `stado plugin bundle`) into the same
// bundledplugins registry that upstream-shipped bundled plugins
// (auto-compact etc.) use. From the runtime's perspective, user-
// bundled plugins are indistinguishable from upstream-shipped ones.
package userbundled

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/bundlepayload"
)

// Bundler exposes the verified bundler pubkey to the rest of the
// process (for --version + bundle info). Set during init(); nil
// when the binary has no bundle or loading failed.
var Bundler ed25519.PublicKey

// SkipVerifyApplied is true when the runtime honoured
// --unsafe-skip-bundle-verify / STADO_UNSAFE_SKIP_BUNDLE_VERIFY.
var SkipVerifyApplied bool

func init() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado: warn: locate executable: %v\n", err)
		return
	}
	skip := os.Getenv("STADO_UNSAFE_SKIP_BUNDLE_VERIFY") == "1"
	if err := loadAndRegister(exe, skip); err != nil {
		fmt.Fprintf(os.Stderr, "stado: ERROR: user-bundled payload invalid: %v\n", err)
		fmt.Fprintf(os.Stderr, "stado: hint: re-bundle, or boot with --unsafe-skip-bundle-verify (loses tamper-evidence)\n")
	}
}

func loadAndRegister(path string, skip bool) error {
	bundle, err := bundlepayload.LoadFromFile(path, skip)
	if err != nil {
		return err
	}
	Bundler = bundle.BundlerPubkey
	SkipVerifyApplied = bundle.SkipVerified
	if SkipVerifyApplied {
		fmt.Fprintln(os.Stderr, "stado: WARNING: bundle signature verification skipped via --unsafe-skip-bundle-verify")
	}
	for _, e := range bundle.Entries {
		bare := strings.TrimPrefix(e.Manifest.Name, bundledplugins.ManifestNamePrefix+"-")
		toolNames := make([]string, 0, len(e.Manifest.Tools))
		for _, t := range e.Manifest.Tools {
			toolNames = append(toolNames, t.Name)
		}
		bundledplugins.RegisterModule(bundledplugins.Info{
			Name:         bare,
			Version:      e.Manifest.Version,
			Author:       e.Manifest.Author,
			Capabilities: e.Manifest.Capabilities,
			Tools:        toolNames,
			WasmSource:   e.Wasm,
		})
	}
	return nil
}
```

- [ ] **Step 5: Wire userbundled into the binary's startup chain**

In `cmd/stado/main.go` (or wherever the main package's imports
are aggregated), add a blank import:

```go
import (
    // ... existing ...
    _ "github.com/foobarto/stado/internal/userbundled"
)
```

This forces the userbundled init() to run during stado startup.
Check if there's already a similar block of blank imports for
bundled-plugin registration — if so, slot it alongside.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/userbundled/ -count=1 -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add internal/userbundled/init.go internal/userbundled/init_test.go internal/bundledplugins/list.go cmd/stado/main.go
git commit -m "feat(userbundled): runtime init registers user-bundled plugins"
```

---

### Task 5: `cmd/stado/plugin_bundle.go` — bundle action (CLI args)

**Files:**
- Create: `cmd/stado/plugin_bundle.go`
- Modify: `cmd/stado/plugin.go` (register pluginBundleCmd)

This task adds the bundle action with CLI-arg plugin selection. TOML manifest support (Task 6) and strip/info actions (Task 7) come later.

- [ ] **Step 1: Read existing plugin command registration patterns**

Run: `grep -n "AddCommand\|pluginCmd" cmd/stado/plugin.go | head -10`

Locate where existing plugin subcommands (install, sign, trust, etc.) are registered.

- [ ] **Step 2: Write `plugin_bundle.go`**

Create `cmd/stado/plugin_bundle.go`:

```go
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/bundlepayload"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
)

var (
	pluginBundleAllowUnsigned bool
	pluginBundleAllowShadow   bool
	pluginBundleFrom          string
	pluginBundleOut           string
	pluginBundleBundlingKey   string
)

var pluginBundleCmd = &cobra.Command{
	Use:   "bundle <plugin-id>...",
	Short: "Bundle installed plugins into a portable stado binary (no Go toolchain required)",
	Long: `bundle copies the source stado binary, then appends the named
installed plugins (their wasm, manifest, and signature) to the tail
of the output. The result is a self-contained custom stado that
ships with those plugins built in.

The appended payload is signed end-to-end with a bundling key
(ephemeral by default; use --bundling-key=path/to/seed for a
persistent identity). At startup, the resulting binary verifies
the bundler signature and each plugin's author signature; tampering
fails the chain and the bundle refuses to load (unless the operator
boots with --unsafe-skip-bundle-verify).

Use --strip to remove the bundle from a customized binary.
Use --info to inspect what's bundled in a binary.`,
	Args: cobra.ArbitraryArgs,
	RunE: runPluginBundle,
}

func runPluginBundle(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one plugin id required (or use --strip / --info)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	from := pluginBundleFrom
	if from == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate running stado: %w", err)
		}
		from = exe
	}
	out := pluginBundleOut
	if out == "" {
		out = filepath.Base(from) + "-custom"
	}

	entries, err := buildEntries(cfg, args, pluginBundleAllowUnsigned)
	if err != nil {
		return err
	}
	if err := checkShadowing(entries, pluginBundleAllowShadow); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Manifest.Name < entries[j].Manifest.Name
	})

	bundlerPub, bundlerPriv, err := loadOrGenerateBundlerKey(pluginBundleBundlingKey)
	if err != nil {
		return fmt.Errorf("bundler key: %w", err)
	}

	if err := bundlepayload.AppendToBinary(from, out, entries, bundlerPriv, bundlerPub); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "bundled %d plugins → %s\n", len(entries), out)
	fmt.Fprintf(cmd.OutOrStdout(), "bundler fingerprint: %s\n", plugins.Fingerprint(bundlerPub)[:16])
	return nil
}

// buildEntries resolves bare plugin IDs to install dirs, reads each
// manifest + sig + wasm, recovers the verifying pubkey (from the
// trust store or <install-dir>/author.pubkey), and returns Entry
// values ready for AppendToBinary. When allowUnsigned is true,
// per-plugin signature verification is skipped.
func buildEntries(cfg *config.Config, ids []string, allowUnsigned bool) ([]bundlepayload.Entry, error) {
	ts := plugins.NewTrustStore(cfg.StateDir())
	var out []bundlepayload.Entry
	for _, id := range ids {
		dir, ok := runtime.ResolveInstalledPluginDir(cfg, id)
		if !ok {
			return nil, fmt.Errorf("plugin %q not installed; run `stado plugin list` to see options", id)
		}
		mf, sigB64, err := plugins.LoadFromDir(dir)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: load: %w", id, err)
		}
		pubkey, err := recoverPubkey(ts, dir, mf)
		if err != nil && !allowUnsigned {
			return nil, fmt.Errorf("plugin %q: %w (pass --allow-unsigned to skip per-plugin verification)", id, err)
		}
		if !allowUnsigned {
			if err := ts.VerifyManifest(mf, sigB64); err != nil {
				return nil, fmt.Errorf("plugin %q: %w", id, err)
			}
		}
		wasm, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
		if err != nil {
			return nil, fmt.Errorf("plugin %q: read wasm: %w", id, err)
		}
		// sigB64 is base64; decode for raw embedding.
		sigRaw, err := decodeBase64Sig(sigB64)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: decode sig: %w", id, err)
		}
		out = append(out, bundlepayload.Entry{
			Pubkey:   pubkey,
			Manifest: *mf,
			Sig:      sigRaw,
			Wasm:     wasm,
		})
	}
	return out, nil
}

// recoverPubkey looks up the manifest's signer pubkey in the
// operator's trust store; falls back to <install-dir>/author.pubkey.
// Returns an error when neither yields a pubkey (caller decides
// whether to honour --allow-unsigned).
func recoverPubkey(ts *plugins.TrustStore, installDir string, mf *plugins.Manifest) (ed25519.PublicKey, error) {
	entries, err := ts.Load()
	if err == nil {
		if entry, ok := entries[mf.AuthorPubkeyFpr]; ok {
			pub, err := plugins.ParsePubkey(entry.PubkeyBase64)
			if err == nil {
				return pub, nil
			}
		}
	}
	// Fallback: <install-dir>/author.pubkey
	pubkeyPath := filepath.Join(installDir, "author.pubkey")
	data, err := os.ReadFile(pubkeyPath)
	if err == nil {
		return plugins.ParsePubkey(strings.TrimSpace(string(data)))
	}
	return nil, fmt.Errorf("verifying pubkey not found in trust store and no author.pubkey on disk")
}

// checkShadowing refuses entries whose declared tools collide with
// already-registered upstream-bundled tools, unless allowShadow is
// true. This catches problems before they manifest as silent
// init-order races at runtime.
func checkShadowing(entries []bundlepayload.Entry, allowShadow bool) error {
	if allowShadow {
		return nil
	}
	registered := map[string]string{}
	for _, info := range bundledplugins.List() {
		for _, t := range info.Tools {
			registered[t] = info.Name
		}
	}
	for _, e := range entries {
		for _, td := range e.Manifest.Tools {
			if owner, ok := registered[td.Name]; ok {
				return fmt.Errorf("tool %q (in %s) collides with already-bundled %s; pass --allow-shadow to override",
					td.Name, e.Manifest.Name, owner)
			}
		}
	}
	return nil
}

// loadOrGenerateBundlerKey: empty path = ephemeral keypair;
// otherwise reads the seed from path. Returns (pub, priv, err).
func loadOrGenerateBundlerKey(seedPath string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if seedPath == "" {
		return ed25519.GenerateKey(rand.Reader)
	}
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read seed %s: %w", seedPath, err)
	}
	if len(seed) < ed25519.SeedSize {
		return nil, nil, fmt.Errorf("seed file too short (%d bytes)", len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	return priv.Public().(ed25519.PublicKey), priv, nil
}

// decodeBase64Sig decodes the base64-encoded signature stored in
// plugin.manifest.sig.
func decodeBase64Sig(sigB64 string) ([]byte, error) {
	return plugins.DecodeBase64Sig(strings.TrimSpace(sigB64))
}

func init() {
	pluginBundleCmd.Flags().BoolVar(&pluginBundleAllowUnsigned, "allow-unsigned", false,
		"Skip per-plugin signature verification (the bundler signature still seals the result)")
	pluginBundleCmd.Flags().BoolVar(&pluginBundleAllowShadow, "allow-shadow", false,
		"Allow bundled plugins to collide with already-registered tool names")
	pluginBundleCmd.Flags().StringVar(&pluginBundleFrom, "from", "",
		"Source stado binary (default: the running stado)")
	pluginBundleCmd.Flags().StringVar(&pluginBundleOut, "out", "",
		"Output path for the customized binary (default: <source-name>-custom)")
	pluginBundleCmd.Flags().StringVar(&pluginBundleBundlingKey, "bundling-key", "",
		"Path to a persistent Ed25519 seed file (default: ephemeral keypair per invocation)")

	pluginCmd.AddCommand(pluginBundleCmd)
}
```

**Note:** this task assumes `plugins.ParsePubkey` and
`plugins.DecodeBase64Sig` are exported — if not (the function in
`internal/plugins/trust.go:220` is `parsePubkey` lowercase),
expose them by renaming or wrapping. Check before writing this
task; if they're unexported, add wrapper functions in
`internal/plugins/exports.go` rather than renaming existing ones.

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 4: Quick sanity smoke (non-test)**

Run: `go run ./cmd/stado plugin bundle --help`
Expected: shows the bundle subcommand help with all 5 flags.

- [ ] **Step 5: Commit**

```bash
git add cmd/stado/plugin_bundle.go cmd/stado/plugin.go internal/plugins/  # if exports added
git commit -m "feat(cli): plugin bundle (CLI-arg selection)"
```

---

### Task 6: TOML manifest support (`--from-file`)

**Files:**
- Modify: `cmd/stado/plugin_bundle.go`

- [ ] **Step 1: Add the flag + parser**

Append to `cmd/stado/plugin_bundle.go`:

```go
var pluginBundleFromFile string

// bundleFile is the in-memory shape of bundle.toml.
type bundleFile struct {
	Output        string `koanf:"output"`
	AllowUnsigned bool   `koanf:"allow_unsigned"`
	Plugins       []struct {
		Name    string `koanf:"name"`
		Version string `koanf:"version,omitempty"`
	} `koanf:"plugin"`
}

func loadBundleFile(path string) (*bundleFile, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
		return nil, err
	}
	var bf bundleFile
	if err := k.Unmarshal("", &bf); err != nil {
		return nil, err
	}
	return &bf, nil
}
```

Imports to add: `github.com/knadh/koanf/v2`, `github.com/knadh/koanf/parsers/toml`, `github.com/knadh/koanf/providers/file` (already present in the project).

- [ ] **Step 2: Wire `--from-file` into runPluginBundle**

At the top of `runPluginBundle`, before the existing `args` check, insert:

```go
if pluginBundleFromFile != "" {
    bf, err := loadBundleFile(pluginBundleFromFile)
    if err != nil {
        return fmt.Errorf("read %s: %w", pluginBundleFromFile, err)
    }
    if pluginBundleOut == "" && bf.Output != "" {
        pluginBundleOut = bf.Output
    }
    if bf.AllowUnsigned {
        pluginBundleAllowUnsigned = true
    }
    for _, p := range bf.Plugins {
        // For now ignore version — pickActiveVersion in
        // ResolveInstalledPluginDir will respect the marker file.
        // Future enhancement: respect explicit pin from bf.Plugins[i].Version.
        args = append(args, p.Name)
    }
}
```

- [ ] **Step 3: Register the flag**

In init():

```go
pluginBundleCmd.Flags().StringVar(&pluginBundleFromFile, "from-file", "",
    "Path to a TOML manifest listing plugins to bundle (alternative to CLI args)")
```

- [ ] **Step 4: Add a test**

Append to `cmd/stado/plugin_bundle_test.go` (creating it if it
doesn't exist):

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./cmd/stado/ -run TestLoadBundleFile -count=1 -v && go build ./...`
Expected: PASS + clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/stado/plugin_bundle.go cmd/stado/plugin_bundle_test.go
git commit -m "feat(cli): plugin bundle --from-file TOML manifest"
```

---

### Task 7: Strip + info actions

**Files:**
- Modify: `cmd/stado/plugin_bundle.go`

- [ ] **Step 1: Add `--strip` and `--info` flags**

In `cmd/stado/plugin_bundle.go`, add to the package-level vars:

```go
var (
    pluginBundleStripFlag bool
    pluginBundleInfoFlag  bool
)
```

Register in init():

```go
pluginBundleCmd.Flags().BoolVar(&pluginBundleStripFlag, "strip", false,
    "Remove the appended bundle from --from, writing vanilla output to --out")
pluginBundleCmd.Flags().BoolVar(&pluginBundleInfoFlag, "info", false,
    "Print the bundle's contents (default --from: running stado)")
```

- [ ] **Step 2: Wire `--strip` and `--info` into runPluginBundle**

At the top of runPluginBundle (after the cfg-load), add:

```go
if pluginBundleStripFlag {
    return runStripAction(cmd)
}
if pluginBundleInfoFlag {
    return runInfoAction(cmd)
}
```

- [ ] **Step 3: Implement runStripAction**

Append to `cmd/stado/plugin_bundle.go`:

```go
func runStripAction(cmd *cobra.Command) error {
    from := pluginBundleFrom
    if from == "" {
        return fmt.Errorf("--from required for --strip (the binary to strip)")
    }
    out := pluginBundleOut
    if out == "" {
        out = filepath.Base(from) + "-stripped"
    }
    if err := bundlepayload.StripFromBinary(from, out); err != nil {
        return err
    }
    fmt.Fprintf(cmd.OutOrStdout(), "stripped → %s\n", out)
    return nil
}
```

- [ ] **Step 4: Implement runInfoAction**

```go
func runInfoAction(cmd *cobra.Command) error {
    from := pluginBundleFrom
    if from == "" {
        exe, err := os.Executable()
        if err != nil {
            return fmt.Errorf("locate running stado: %w", err)
        }
        from = exe
    }
    bundle, err := bundlepayload.LoadFromFile(from, false)
    if err != nil {
        return fmt.Errorf("read bundle: %w", err)
    }
    if len(bundle.Entries) == 0 {
        fmt.Fprintf(cmd.OutOrStdout(), "%s: no bundle (vanilla stado)\n", from)
        return nil
    }
    fmt.Fprintf(cmd.OutOrStdout(), "%s\n", from)
    fmt.Fprintf(cmd.OutOrStdout(), "  Bundler:  %s\n", plugins.Fingerprint(bundle.BundlerPubkey)[:16])
    fmt.Fprintf(cmd.OutOrStdout(), "  Plugins (%d):\n", len(bundle.Entries))
    for _, e := range bundle.Entries {
        bare := strings.TrimPrefix(e.Manifest.Name, bundledplugins.ManifestNamePrefix+"-")
        fmt.Fprintf(cmd.OutOrStdout(), "    • %-20s v%-10s  %d tools, %d KB wasm\n",
            bare, e.Manifest.Version, len(e.Manifest.Tools), len(e.Wasm)/1024)
    }
    return nil
}
```

- [ ] **Step 5: Add an integration test for round-trip**

Append to `cmd/stado/plugin_bundle_test.go`:

```go
import (
    "github.com/foobarto/stado/internal/bundlepayload"
)

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
        // Build one entry with proper sig
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

// sampleBundleEntry is a helper that constructs a properly-signed
// Entry for tests. (Keeps each test from re-doing the keygen + sig
// dance.)
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
```

- [ ] **Step 6: Run tests + build**

Run: `go test ./cmd/stado/ -count=1 && go build ./...`
Expected: PASS + clean.

- [ ] **Step 7: Commit**

```bash
git add cmd/stado/plugin_bundle.go cmd/stado/plugin_bundle_test.go
git commit -m "feat(cli): plugin bundle --strip + --info actions"
```

---

### Task 8: `--unsafe-skip-bundle-verify` top-level flag + `--version` integration

**Files:**
- Modify: `cmd/stado/main.go` (or wherever the cobra root is)
- Modify: `cmd/stado/version.go` (or wherever --version output is formatted)

- [ ] **Step 1: Locate the cobra root + version file**

Run: `grep -rn "rootCmd\|RootCmd\|version.Stado" cmd/stado/*.go | head -10`

Identify where the root cobra command is defined (look for `rootCmd := &cobra.Command{...}` or similar) and where the version string is formatted.

- [ ] **Step 2: Add the persistent flag at the cobra root**

In whatever file defines the root command, add:

```go
var unsafeSkipBundleVerify bool

func init() {
    // Top-level flag — applies to all subcommands and the root
    // (--version) handler. The flag's value is mirrored into the
    // STADO_UNSAFE_SKIP_BUNDLE_VERIFY env var BEFORE any
    // userbundled.init() runs so the runtime path picks it up.
    rootCmd.PersistentFlags().BoolVar(&unsafeSkipBundleVerify, "unsafe-skip-bundle-verify", false,
        "Skip runtime verification of the appended user-bundled payload (loses tamper-evidence)")
    cobra.OnInitialize(func() {
        if unsafeSkipBundleVerify {
            os.Setenv("STADO_UNSAFE_SKIP_BUNDLE_VERIFY", "1")
        }
    })
}
```

**However:** `userbundled.init()` runs BEFORE cobra parses flags
(Go init() ordering: package-level inits run at program startup,
before main()). So the flag-value mirroring above can't change
the runtime registration that already happened.

The fix: detect the flag early, before cobra dispatches. Options:

- **A.** Scan `os.Args` in `userbundled.init()` for `--unsafe-skip-bundle-verify` directly. Brittle but works.
- **B.** Move the userbundled registration out of init() into an explicit call from main.go AFTER cobra flag parsing. Means `BuildDefaultRegistry` and friends need to call this hook before they consult `bundledplugins.List()`.
- **C.** Honor the env var at init() time AND also re-scan os.Args.

**Pick A.** Simplest. The flag is rare enough that brittleness is acceptable, and any false positive (someone happens to have a string `--unsafe-skip-bundle-verify` in an arg they pass) is the operator's choice.

In `internal/userbundled/init.go`, change the env-var detection to also walk `os.Args`:

```go
func detectSkipVerify() bool {
    if os.Getenv("STADO_UNSAFE_SKIP_BUNDLE_VERIFY") == "1" {
        return true
    }
    for _, a := range os.Args[1:] {
        if a == "--unsafe-skip-bundle-verify" {
            return true
        }
    }
    return false
}

func init() {
    // ...
    skip := detectSkipVerify()
    // ...
}
```

Keep the rootCmd flag for cobra UX (visible in --help), and keep
the env var for completeness.

- [ ] **Step 3: Update --version output**

In the version formatter, detect bundled state and append markers:

```go
import (
    "github.com/foobarto/stado/internal/userbundled"
    "github.com/foobarto/stado/internal/plugins"
)

func formatVersion() string {
    base := fmt.Sprintf("stado %s", version.Stado)
    if userbundled.Bundler != nil {
        fpr := plugins.Fingerprint(userbundled.Bundler)
        // Count user-bundled (those with WasmSource non-nil)
        var n int
        for _, info := range bundledplugins.List() {
            if info.WasmSource != nil { n++ }
        }
        base += fmt.Sprintf(" (custom: %d plugins, bundler=%s)", n, fpr[:8])
    }
    if userbundled.SkipVerifyApplied {
        base += " [unsafe-skip-verify]"
    }
    return base
}
```

Match the function name to whatever the existing code uses (could
be `versionString()`, an inline string in a `Run`, etc.). The
critical pieces are: (a) consult userbundled.Bundler / SkipVerifyApplied;
(b) append the markers to the existing version output.

- [ ] **Step 4: Add a test for the version-output format**

If there's an existing test for version output, append cases:

```go
func TestFormatVersion_Vanilla(t *testing.T) {
    userbundled.Bundler = nil
    userbundled.SkipVerifyApplied = false
    got := formatVersion()
    if strings.Contains(got, "custom") || strings.Contains(got, "unsafe") {
        t.Errorf("vanilla version should not contain markers; got %q", got)
    }
}
```

(If no existing test infrastructure for `formatVersion`, skip
adding one — the manual smoke covers it.)

- [ ] **Step 5: Run tests + build**

Run: `go test ./cmd/stado/ -count=1 && go build ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/stado/main.go cmd/stado/version.go internal/userbundled/init.go
git commit -m "feat(cli): --unsafe-skip-bundle-verify + custom version marker"
```

---

### Task 9: End-to-end smoke + handoff

**Files:**
- Modify: `docs/superpowers/specs/2026-05-06-plugin-bundle-design.md`

- [ ] **Step 1: Build a test stado**

Run: `go build -o /tmp/stado-base ./cmd/stado`
Expected: clean build.

- [ ] **Step 2: Bundle a real plugin**

Pick an installed plugin from `~/.local/share/stado/plugins/`
(e.g. `htb-lab` or `gtfobins`):

Run: `/tmp/stado-base plugin bundle htb-lab --out=/tmp/stado-htb`
Expected: prints `bundled 1 plugins → /tmp/stado-htb` + bundler fingerprint.

- [ ] **Step 3: Verify the custom binary works**

Run: `/tmp/stado-htb --version`
Expected: includes `(custom: 1 plugins, bundler=<fpr>)`.

Run: `/tmp/stado-htb tool list | grep htb`
Expected: htb-lab tools listed.

Run: `/tmp/stado-htb plugin bundle --info`
Expected: prints bundler fingerprint + plugin list.

- [ ] **Step 4: Strip round-trip**

Run: `/tmp/stado-htb plugin bundle --strip --from=/tmp/stado-htb --out=/tmp/stado-stripped`
Run: `cmp /tmp/stado-base /tmp/stado-stripped`
Expected: files identical (`cmp` exits 0, no output).

- [ ] **Step 5: Skip-verify smoke**

Run: `dd if=/dev/urandom of=/tmp/stado-htb bs=1 count=1 seek=$(($(stat -c%s /tmp/stado-htb) - 100)) conv=notrunc`
(corrupts a byte mid-payload)

Run: `/tmp/stado-htb --version 2>&1 | grep -i error`
Expected: ERROR about invalid bundle.

Run: `/tmp/stado-htb --unsafe-skip-bundle-verify --version`
Expected: boots, prints loud WARNING, version ends with `[unsafe-skip-verify]`.

- [ ] **Step 6: Append handoff to the spec**

Add a `## Handoff (2026-05-06)` section at the end of the spec
file documenting:
- What shipped (commits + features)
- What was tested (per the smoke steps above)
- Any deviations from the spec
- What to watch in production

- [ ] **Step 7: Commit**

```bash
git add docs/superpowers/specs/2026-05-06-plugin-bundle-design.md
git commit -m "docs(specs): plugin bundle handoff"
```

---

## Verification checklist (after Task 9)

- [ ] `go test ./... -count=1` — all packages pass except known
      pre-existing failures.
- [ ] `go vet ./...` — clean.
- [ ] `go run ./cmd/stado plugin bundle --help` — shows all flags.
- [ ] Smoke per Task 9 succeeded end-to-end.
- [ ] `/tmp/stado-base` (vanilla) and `/tmp/stado-stripped` are
      byte-identical (cmp returns 0).
- [ ] Tampered binary refuses to load without `--unsafe-skip-bundle-verify`.
- [ ] Bundled binary's version string includes `(custom: N plugins, bundler=<fpr>)`.
