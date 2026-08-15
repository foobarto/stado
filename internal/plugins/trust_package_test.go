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
)

func signedPackageForTrustTest(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, name, version string) (*Manifest, string) {
	t.Helper()
	m := &Manifest{Name: name, Version: version, Author: "official", AuthorPubkeyFpr: Fingerprint(pub)}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	return m, sig
}

func TestPackageRollbackFloorsAreIndependentForOneSigner(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	a2, a2sig := signedPackageForTrustTest(t, pub, priv, "alpha", "2.0.0")
	b1, b1sig := signedPackageForTrustTest(t, pub, priv, "beta", "1.0.0")
	if _, err := store.TrustVerifiedPackage(hex.EncodeToString(pub), "official", "github.com/acme/plugins/alpha", a2, a2sig); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrustVerifiedPackage(hex.EncodeToString(pub), "official", "github.com/acme/plugins/beta", b1, b1sig); err != nil {
		t.Fatalf("a second, independently-versioned package was rejected: %v", err)
	}
	a1, a1sig := signedPackageForTrustTest(t, pub, priv, "alpha", "1.0.0")
	if err := store.VerifyManifestPackage("github.com/acme/plugins/alpha", a1, a1sig); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("alpha rollback error = %v", err)
	}
	entries, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	floors := entries[Fingerprint(pub)].VersionFloors
	if floors["github.com/acme/plugins/alpha"] != "2.0.0" || floors["github.com/acme/plugins/beta"] != "1.0.0" {
		t.Fatalf("package floors = %#v", floors)
	}
}

func TestTrustVerifiedAnchorCommitsOwnerSignerAndFloorTogether(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	m, sig := signedPackageForTrustTest(t, pub, priv, "alpha", "1.0.0")
	if _, err := store.TrustVerifiedAnchor(hex.EncodeToString(pub), "official", "github.com/acme/plugins/alpha", "github.com/acme", m, strings.Repeat("A", 88)); err == nil {
		t.Fatal("invalid signature unexpectedly committed trust")
	}
	if entries, err := store.Load(); err != nil || len(entries) != 0 {
		t.Fatalf("failed transaction left trust behind: entries=%#v err=%v", entries, err)
	}
	entry, err := store.TrustVerifiedAnchor(hex.EncodeToString(pub), "official", "github.com/acme/plugins/alpha", "github.com/acme", m, sig)
	if err != nil {
		t.Fatal(err)
	}
	anchor, ok, err := store.AnchorSigner("github.com/acme")
	if err != nil || !ok || anchor.Fingerprint != entry.Fingerprint {
		t.Fatalf("anchor signer = %#v ok=%v err=%v", anchor, ok, err)
	}
	if anchor.VersionFloors["github.com/acme/plugins/alpha"] != "1.0.0" {
		t.Fatalf("anchor and floor were not committed together: %#v", anchor)
	}
}

func TestTrustVerifiedAnchorRejectsSecondSignerWithoutPartialRotation(t *testing.T) {
	firstPub, firstPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPub, secondPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	first, firstSig := signedPackageForTrustTest(t, firstPub, firstPriv, "alpha", "1.0.0")
	if _, err := store.TrustVerifiedAnchor(hex.EncodeToString(firstPub), "official", "github.com/acme/plugins/alpha", "github.com/acme", first, firstSig); err != nil {
		t.Fatal(err)
	}
	second, secondSig := signedPackageForTrustTest(t, secondPub, secondPriv, "beta", "1.0.0")
	if _, err := store.TrustVerifiedAnchor(hex.EncodeToString(secondPub), "official", "github.com/acme/plugins/beta", "github.com/acme", second, secondSig); err == nil {
		t.Fatal("a second signer unexpectedly captured the existing owner anchor")
	}
	entries, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[Fingerprint(firstPub)].VersionFloors["github.com/acme/plugins/alpha"] != "1.0.0" {
		t.Fatalf("failed rotation partially changed trust: %#v", entries)
	}
}

func TestCheckInstalledPackageLocalIdentityAndCurrentAdmissionAreReadOnly(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	source := filepath.Join(state, "src", "demo")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	m, sig := signedPackageForTrustTest(t, pub, priv, "demo", "1.0.0")
	record, err := NewLocalInstallRecord(source, *m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "plugins", record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallRecord(dir, record, *m); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallReceipt(state, filepath.Join(state, "plugins"), record); err != nil {
		t.Fatal(err)
	}
	identity, err := RuntimeIdentityForLocalSource(*m, source)
	if err != nil {
		t.Fatal(err)
	}
	store := NewTrustStore(state)
	if _, err := store.TrustVerifiedPackage(hex.EncodeToString(pub), "local", identity.Namespace, m, sig); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.CheckInstalledPackage(dir, m, sig)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got != identity || !bytes.Equal(before, after) {
		t.Fatalf("identity=%#v want=%#v trust changed=%v", got, identity, !bytes.Equal(before, after))
	}
}

func TestCheckInstalledPackageUsesExactRemoteLockNamespace(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	m, _ := signedPackageForTrustTest(t, pub, priv, "supervise", "1.0.0")
	m.WASMSHA256 = "remote-wasm"
	// Re-sign after binding the digest.
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseIdentity("github.com/acme/stado-plugins/supervise@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	entry := mustResolvedLockEntry(t, id, "supervise/v1.0.0", "0123456789abcdef0123456789abcdef01234567", *m)
	record, err := NewRemoteInstallRecord(id, entry.SourceRevision, entry.ResolvedCommit, *m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "plugins", record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallRecord(dir, record, *m); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallReceipt(state, filepath.Join(state, "plugins"), record); err != nil {
		t.Fatal(err)
	}
	lock := &Lock{Entries: []LockEntry{entry}}
	if err := lock.Write(filepath.Join(state, "plugin-lock.toml")); err != nil {
		t.Fatal(err)
	}
	store := NewTrustStore(state)
	if _, err := store.TrustVerifiedAnchor(hex.EncodeToString(pub), "official", id.Namespace(), id.OwnerKey(), m, sig); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CheckInstalledPackage(dir, m, sig)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Namespace != id.Namespace() || identity.SourceRevision != "supervise/v1.0.0" {
		t.Fatalf("remote identity = %#v", identity)
	}
}

func TestCheckInstalledPackageMalformedMatchingLockNeverFallsBackLocal(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	m, _ := signedPackageForTrustTest(t, pub, priv, "supervise", "1.0.0")
	m.WASMSHA256 = "remote-wasm"
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	id, err := ParseIdentity("github.com/acme/stado-plugins/supervise@" + commit)
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewRemoteInstallRecord(id, commit, commit, *m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "plugins", record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallRecord(dir, record, *m); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallReceipt(state, filepath.Join(state, "plugins"), record); err != nil {
		t.Fatal(err)
	}
	lock := &Lock{Entries: []LockEntry{{
		StoreKey:       record.StoreKey,
		Identity:       "github.com/acme/stado-plugins/supervise@" + commit,
		SourceRevision: commit, WASMSHA256: m.WASMSHA256,
	}}}
	if err := lock.Write(filepath.Join(state, "plugin-lock.toml")); err != nil {
		t.Fatal(err)
	}
	store := NewTrustStore(state)
	if _, err := store.Trust(hex.EncodeToString(pub), "official"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckInstalledPackage(dir, m, sig); err == nil || !strings.Contains(err.Error(), "does not exactly match") {
		t.Fatalf("malformed matching lock error = %v", err)
	}
}

func TestCheckInstalledPackageEnforcesExactPackageFloor(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	id1, err := ParseIdentity("github.com/acme/stado-plugins/supervise@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	low, _ := signedPackageForTrustTest(t, pub, priv, "supervise", "1.0.0")
	low.WASMSHA256 = "remote-wasm"
	lowSig, err := low.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	entry := mustResolvedLockEntry(t, id1, "supervise/v1.0.0", "0123456789abcdef0123456789abcdef01234567", *low)
	record, err := NewRemoteInstallRecord(id1, entry.SourceRevision, entry.ResolvedCommit, *low)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "plugins", record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallRecord(dir, record, *low); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallReceipt(state, filepath.Join(state, "plugins"), record); err != nil {
		t.Fatal(err)
	}
	lock := &Lock{Entries: []LockEntry{entry}}
	if err := lock.Write(filepath.Join(state, "plugin-lock.toml")); err != nil {
		t.Fatal(err)
	}
	high, highSig := signedPackageForTrustTest(t, pub, priv, "supervise", "2.0.0")
	store := NewTrustStore(state)
	if _, err := store.TrustVerifiedAnchor(hex.EncodeToString(pub), "official", id1.Namespace(), id1.OwnerKey(), high, highSig); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckInstalledPackage(dir, low, lowSig); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("installed rollback error = %v", err)
	}
}

func TestProjectRecordAndLockCannotMintRemoteSourceAuthority(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	project := filepath.Join(t.TempDir(), ".stado")
	root := filepath.Join(project, "plugins")
	m, _ := signedPackageForTrustTest(t, pub, priv, "borrowed-name", "1.0.0")
	m.WASMSHA256 = strings.Repeat("1", 64)
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseIdentity("github.com/attacker/fabricated/borrowed@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	entry := mustResolvedLockEntry(t, id, "borrowed/v1.0.0", "0123456789abcdef0123456789abcdef01234567", *m)
	record, err := NewRemoteInstallRecord(id, entry.SourceRevision, entry.ResolvedCommit, *m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, record.StoreKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallRecord(dir, record, *m); err != nil {
		t.Fatal(err)
	}
	if err := (&Lock{Entries: []LockEntry{entry}}).Write(filepath.Join(project, "plugin-lock.toml")); err != nil {
		t.Fatal(err)
	}
	store := NewTrustStore(state)
	// The signer is globally trusted for an unrelated exact local package.
	// The repository itself performs no trust-store mutation.
	if _, err := store.TrustVerifiedPackage(hex.EncodeToString(pub), "known signer", "local://sha256/"+strings.Repeat("a", 64), m, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckInstalledPackage(dir, m, sig); err == nil || !strings.Contains(err.Error(), "host-owned exact install receipt") {
		t.Fatalf("fabricated project admission error = %v", err)
	}
	// Even a host receipt is insufficient for remote authority unless the
	// exact repository owner was accepted for this signer.
	if err := WriteInstallReceipt(state, root, record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckInstalledPackage(dir, m, sig); err == nil || !strings.Contains(err.Error(), "owner-anchor") {
		t.Fatalf("unanchored fabricated source error = %v", err)
	}
}
