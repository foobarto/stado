package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/pkg/tool"
)

func TestManifest_CanonicalIsStable(t *testing.T) {
	m := &Manifest{
		Name:            "demo",
		Version:         "1.0.0",
		Author:          "alice",
		Capabilities:    []string{"fs:read:.", "net:api.example.com"},
		Tools:           []ToolDef{{Name: "run", Description: "run the demo"}},
		MinStadoVersion: "0.1.0",
	}
	a, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Canonical()
	if !bytes.Equal(a, b) {
		t.Errorf("canonicalise not deterministic:\n%s\n%s", a, b)
	}
	// Keys should be sorted.
	s := string(a)
	if strings.Index(s, `"author":`) > strings.Index(s, `"capabilities":`) {
		t.Errorf("keys not sorted: %s", s)
	}
}

func TestManifest_SignVerify_RoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{Name: "t", Version: "0.1.0", AuthorPubkeyFpr: Fingerprint(pub)}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(pub, sig); err != nil {
		t.Errorf("verify failed: %v", err)
	}
}

func TestManifest_TamperDetected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{Name: "original", Version: "0.1.0"}
	sig, _ := m.Sign(priv)
	m.Name = "tampered"
	if err := m.Verify(pub, sig); err == nil {
		t.Error("verify should reject tampered manifest")
	}
}

func TestTrustStore_TrustAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}

	entry, err := ts.Trust(hex.EncodeToString(pub), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Fingerprint == "" || entry.Author != "alice" {
		t.Errorf("trust entry: %+v", entry)
	}

	m := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: entry.Fingerprint}
	sig, _ := m.Sign(priv)
	if err := ts.VerifyManifest(m, sig); err != nil {
		t.Errorf("VerifyManifest: %v", err)
	}
}

func TestTrustStoreSaveDoesNotFollowPredictableTempSymlink(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "trust.json")
	if err := os.Symlink(outside, path+".tmp"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ts := &TrustStore{Path: path}

	if _, err := ts.Trust(hex.EncodeToString(pub), "alice"); err != nil {
		t.Fatalf("trust: %v", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("outside target was modified: %q", got)
	}
	store, err := ts.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(store) != 1 {
		t.Fatalf("trust store entries = %d, want 1", len(store))
	}
}

func TestTrustStore_VerifyRejectsUnpinned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	m := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: Fingerprint(pub)}
	sig, _ := m.Sign(priv)
	err := ts.VerifyManifest(m, sig)
	if err == nil {
		t.Error("expected verify to fail for unpinned author")
	}
	if !strings.Contains(err.Error(), "not pinned") {
		t.Errorf("error wrong: %v", err)
	}
}

func TestTrustStore_VerifyRejectsPubkeyFingerprintMismatch(t *testing.T) {
	trustedPub, _, _ := ed25519.GenerateKey(rand.Reader)
	attackerPub, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	trustedFPR := Fingerprint(trustedPub)
	if err := ts.Save(map[string]TrustEntry{
		trustedFPR: {
			Fingerprint: trustedFPR,
			Pubkey:      hex.EncodeToString(attackerPub),
			Author:      "mallory",
			Pinned:      time.Now().UTC(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: trustedFPR}
	sig, _ := m.Sign(attackerPriv)
	err := ts.VerifyManifest(m, sig)
	if err == nil {
		t.Fatal("VerifyManifest should reject a pubkey stored under the wrong fingerprint")
	}
	if !strings.Contains(err.Error(), "pubkey fingerprint mismatch") {
		t.Fatalf("VerifyManifest error = %v, want fingerprint mismatch", err)
	}
}

func TestTrustStore_RollbackProtection(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	_, _ = ts.Trust(hex.EncodeToString(pub), "alice")

	// Install v2.0.0 first.
	m2 := &Manifest{Name: "p", Version: "2.0.0", AuthorPubkeyFpr: Fingerprint(pub)}
	sig2, _ := m2.Sign(priv)
	if err := ts.VerifyManifest(m2, sig2); err != nil {
		t.Fatal(err)
	}
	// Now try to install v1.0.0 — should be blocked.
	m1 := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: Fingerprint(pub)}
	sig1, _ := m1.Sign(priv)
	err := ts.VerifyManifest(m1, sig1)
	if err == nil {
		t.Error("rollback 1.0.0 < 2.0.0 should be blocked")
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Errorf("error should mention rollback: %v", err)
	}
}

func TestTrustStore_TrustPreservesRollbackState(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	entry, err := ts.Trust(hex.EncodeToString(pub), "alice")
	if err != nil {
		t.Fatal(err)
	}

	mHigh := &Manifest{Name: "p", Version: "2.0.0", AuthorPubkeyFpr: entry.Fingerprint}
	sigHigh, _ := mHigh.Sign(priv)
	if err := ts.VerifyManifest(mHigh, sigHigh); err != nil {
		t.Fatal(err)
	}
	retrusted, err := ts.Trust(hex.EncodeToString(pub), "")
	if err != nil {
		t.Fatal(err)
	}
	if retrusted.LastVersion != "2.0.0" {
		t.Fatalf("re-trust LastVersion = %q, want 2.0.0", retrusted.LastVersion)
	}
	if retrusted.Author != "alice" {
		t.Fatalf("re-trust Author = %q, want alice", retrusted.Author)
	}

	mLow := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: entry.Fingerprint}
	sigLow, _ := mLow.Sign(priv)
	err = ts.VerifyManifest(mLow, sigLow)
	if err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("VerifyManifest after re-trust = %v, want rollback rejection", err)
	}
}

func TestTrustStore_TrustVerifiedDoesNotPinMismatchedSigner(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	m := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: Fingerprint(pub1)}
	sig, _ := m.Sign(priv1)

	_, err := ts.TrustVerified(hex.EncodeToString(pub2), "mallory", m, sig)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("TrustVerified mismatch error = %v", err)
	}
	store, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(store) != 0 {
		t.Fatalf("mismatched signer was pinned: %+v", store)
	}
}

func TestTrustStore_TrustVerifiedDoesNotPinInvalidSignature(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	m := &Manifest{Name: "p", Version: "1.0.0", AuthorPubkeyFpr: Fingerprint(pub)}

	_, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", m, strings.Repeat("A", 88))
	if err == nil {
		t.Fatal("TrustVerified should reject invalid signature")
	}
	store, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(store) != 0 {
		t.Fatalf("invalid signature signer was pinned: %+v", store)
	}
}

func TestTrustStore_RollbackProtectionUsesSemverOrdering(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	_, _ = ts.Trust(hex.EncodeToString(pub), "alice")

	mHigh := &Manifest{Name: "p", Version: "1.10.0", AuthorPubkeyFpr: Fingerprint(pub)}
	sigHigh, _ := mHigh.Sign(priv)
	if err := ts.VerifyManifest(mHigh, sigHigh); err != nil {
		t.Fatal(err)
	}

	mLow := &Manifest{Name: "p", Version: "1.2.0", AuthorPubkeyFpr: Fingerprint(pub)}
	sigLow, _ := mLow.Sign(priv)
	err := ts.VerifyManifest(mLow, sigLow)
	if err == nil {
		t.Fatal("rollback 1.2.0 < 1.10.0 should be blocked")
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Errorf("error should mention rollback: %v", err)
	}
}

func TestTrustStore_VerifyRejectsInvalidSemverVersion(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	_, _ = ts.Trust(hex.EncodeToString(pub), "alice")

	m := &Manifest{Name: "p", Version: "build-20260425", AuthorPubkeyFpr: Fingerprint(pub)}
	sig, _ := m.Sign(priv)
	err := ts.VerifyManifest(m, sig)
	if err == nil {
		t.Fatal("invalid semver manifest version should be rejected")
	}
	if !strings.Contains(err.Error(), "semver") {
		t.Errorf("error should mention semver: %v", err)
	}
}

func TestVersionLessSemverPrecedence(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "minor numeric ordering", a: "1.2.0", b: "1.10.0", want: true},
		{name: "prerelease lower than release", a: "1.0.0-rc.1", b: "1.0.0", want: true},
		{name: "build metadata ignored", a: "1.0.0+build.2", b: "1.0.0+build.1", want: false},
		{name: "large numeric prerelease identifiers", a: "1.0.0-99999999999999999999", b: "1.0.0-100000000000000000000", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VersionLess(tt.a, tt.b)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("VersionLess(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestTrustStore_Untrust(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	ts := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	entry, _ := ts.Trust(hex.EncodeToString(pub), "alice")
	if err := ts.Untrust(entry.Fingerprint); err != nil {
		t.Fatal(err)
	}
	store, _ := ts.Load()
	if _, ok := store[entry.Fingerprint]; ok {
		t.Errorf("fingerprint still present after untrust")
	}
	// Untrust again → error (non-existent).
	if err := ts.Untrust(entry.Fingerprint); err == nil {
		t.Error("untrust non-existent should error")
	}
}

func TestVerifyWASMDigest(t *testing.T) {
	dir := t.TempDir()
	body := []byte("pretend-wasm-bytes")
	wasmPath := filepath.Join(dir, "plugin.wasm")
	os.WriteFile(wasmPath, body, 0o644)

	// sha256 of body.
	// echo -n "pretend-wasm-bytes" | sha256sum
	// 37e0ed1ceb1b4ea0e6d0f6d0ccb47f7e19bf3e4cd9b1a43b4e3e7e4aad3d6d5f  (fake; we'll compute)
	m := &Manifest{}
	sig, _ := m.Sign(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))) // dummy
	_ = sig
	// Compute the expected hash by letting VerifyWASMDigest tell us.
	// We pass a wrong hash first to see the error format.
	if err := VerifyWASMDigest("0000000000000000000000000000000000000000000000000000000000000000", wasmPath); err == nil {
		t.Error("expected mismatch error")
	}
}

func TestVerifyWASMDigestRejectsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.wasm")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := os.Symlink(outside, wasmPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := VerifyWASMDigest(strings.Repeat("0", 64), wasmPath)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("VerifyWASMDigest error = %v, want symlink rejection", err)
	}
}

func TestVerifyWASMDigestRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "plugin.wasm"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "plugin-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := VerifyWASMDigest(strings.Repeat("0", 64), filepath.Join(link, "plugin.wasm"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("VerifyWASMDigest error = %v, want symlink rejection", err)
	}
}

func TestReadVerifiedWASMRejectsOversizedWASM(t *testing.T) {
	dir := t.TempDir()
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := os.WriteFile(wasmPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(wasmPath, maxPluginWASMBytes+1); err != nil {
		t.Fatal(err)
	}

	_, err := ReadVerifiedWASM(strings.Repeat("0", 64), wasmPath)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadVerifiedWASM error = %v, want size rejection", err)
	}
}

func TestLoadFromDir(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{
		Name:            "demo",
		Version:         "1.0.0",
		Author:          "alice",
		AuthorPubkeyFpr: Fingerprint(pub),
		WASMSHA256:      "00",
		TimestampUTC:    time.Now().UTC().Format(time.RFC3339),
	}
	canonical, _ := m.Canonical()
	sig, _ := m.Sign(priv)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644)
	os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte(sig), 0o644)

	got, gotSig, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Name != "demo" {
		t.Errorf("name = %q", got.Name)
	}
	if gotSig != sig {
		t.Errorf("sig mismatch")
	}
}

func TestLoadFromDirRejectsOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin.manifest.json")
	if err := os.WriteFile(manifestPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(manifestPath, maxPluginManifestBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("sig"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadFromDir error = %v, want size rejection", err)
	}
}

func TestLoadFromDirRejectsManifestSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"name":"outside"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "plugin.manifest.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("sig"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadFromDir error = %v, want symlink rejection", err)
	}
}

func TestLoadFromDirRejectsOversizedAuthorPubkey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{
		Name:            "demo",
		Version:         "1.0.0",
		Author:          "alice",
		AuthorPubkeyFpr: Fingerprint(pub),
		WASMSHA256:      "00",
		TimestampUTC:    time.Now().UTC().Format(time.RFC3339),
	}
	canonical, _ := m.Canonical()
	sig, _ := m.Sign(priv)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
	pubkeyPath := filepath.Join(dir, "author.pubkey")
	if err := os.WriteFile(pubkeyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(pubkeyPath, maxPluginAuthorPubkeyBytes+1); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadFromDir error = %v, want size rejection", err)
	}
}

func TestLoadFromDirRejectsAuthorPubkeySymlink(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{
		Name:            "demo",
		Version:         "1.0.0",
		Author:          "alice",
		AuthorPubkeyFpr: Fingerprint(pub),
		WASMSHA256:      "00",
		TimestampUTC:    time.Now().UTC().Format(time.RFC3339),
	}
	canonical, _ := m.Canonical()
	sig, _ := m.Sign(priv)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "author.pubkey")
	if err := os.WriteFile(outside, []byte(hex.EncodeToString(pub)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "author.pubkey")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, _, err := LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadFromDir error = %v, want symlink rejection", err)
	}
}

func TestToolDef_ClassValue(t *testing.T) {
	cases := []struct {
		class string
		want  tool.Class
		ok    bool
	}{
		{"", tool.ClassNonMutating, true},
		{"NonMutating", tool.ClassNonMutating, true},
		{"StateMutating", tool.ClassStateMutating, true},
		{"Mutating", tool.ClassMutating, true},
		{"Exec", tool.ClassExec, true},
		{"weird", tool.ClassNonMutating, false},
	}
	for _, tc := range cases {
		td := ToolDef{Name: "demo", Class: tc.class}
		got, err := td.ClassValue()
		if tc.ok {
			if err != nil {
				t.Fatalf("ClassValue(%q): %v", tc.class, err)
			}
			if got != tc.want {
				t.Fatalf("ClassValue(%q) = %v, want %v", tc.class, got, tc.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("ClassValue(%q) should fail", tc.class)
		}
	}
}

func TestParsePubkey_BothFormats(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	hexKey := hex.EncodeToString(pub)
	if _, err := parsePubkey(hexKey); err != nil {
		t.Errorf("hex pubkey rejected: %v", err)
	}
	// base64
	b64 := "YWZvbw" // too short; should error
	if _, err := parsePubkey(b64); err == nil {
		t.Errorf("short base64 should error")
	}
}
