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
