package audit

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

// TestSSHSIG_RoundTripSHA512 asserts the Git-compatible path works:
// sign with SignSSH + "sha512" + "git" namespace, verify the result,
// and recover the signer's pubkey from the blob.
func TestSSHSIG_RoundTripSHA512(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("tree 0123456789abcdef0123456789abcdef01234567\nauthor stado <a@b> 0 +0000\ncommitter stado <a@b> 0 +0000\n\nmsg body\n")

	sshsig, err := SignSSH(priv, GitNamespace, HashSHA512, message)
	if err != nil {
		t.Fatalf("SignSSH: %v", err)
	}
	if !strings.HasPrefix(sshsig, "-----BEGIN SSH SIGNATURE-----") {
		t.Errorf("missing PEM header: %q", sshsig[:64])
	}
	if !strings.Contains(sshsig, "-----END SSH SIGNATURE-----") {
		t.Error("missing PEM footer")
	}

	if err := VerifySSH(pub, GitNamespace, HashSHA512, message, sshsig); err != nil {
		t.Fatalf("VerifySSH: %v", err)
	}

	recovered, err := Ed25519PubFromSSHSIG(sshsig)
	if err != nil {
		t.Fatalf("Ed25519PubFromSSHSIG: %v", err)
	}
	if string(recovered) != string(pub) {
		t.Error("pubkey recovered from blob does not match signer")
	}
}

// TestSSHSIG_RoundTripSHA256 confirms the sha256 path also round-trips
// (not Git-compatible, but listed in the SSHSIG spec).
func TestSSHSIG_RoundTripSHA256(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("x")
	sshsig, err := SignSSH(priv, "test-ns", HashSHA256, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySSH(pub, "test-ns", HashSHA256, msg, sshsig); err != nil {
		t.Errorf("verify: %v", err)
	}
}

// TestSSHSIG_TamperedMessageRejected — flipping a bit in the message
// after signing must fail verification.
func TestSSHSIG_TamperedMessageRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("original message")
	sshsig, _ := SignSSH(priv, GitNamespace, HashSHA512, msg)
	if err := VerifySSH(pub, GitNamespace, HashSHA512, []byte("tampered"), sshsig); err == nil {
		t.Fatal("tampered message should fail verification")
	}
}

// TestSSHSIG_WrongPubkeyRejected — verifier with a different key
// catches the mismatch even if the PEM decodes fine.
func TestSSHSIG_WrongPubkeyRejected(t *testing.T) {
	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("msg")
	sshsig, _ := SignSSH(priv1, GitNamespace, HashSHA512, msg)
	if err := VerifySSH(pub2, GitNamespace, HashSHA512, msg, sshsig); err == nil {
		t.Fatal("verification with wrong pubkey should fail")
	}
}

// TestSSHSIG_NamespaceMismatchRejected — a signature produced for
// namespace X must not verify for namespace Y, even with the right
// pubkey.
func TestSSHSIG_NamespaceMismatchRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("m")
	sshsig, _ := SignSSH(priv, "ns-A", HashSHA512, msg)
	err := VerifySSH(pub, "ns-B", HashSHA512, msg, sshsig)
	if err == nil {
		t.Fatal("namespace mismatch should reject")
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Errorf("error should mention namespace: %v", err)
	}
}

// TestSSHSIG_HashAlgoMismatchRejected — signing with sha512 and
// verifying with sha256 is a mismatch, not a silent pass.
func TestSSHSIG_HashAlgoMismatchRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("m")
	sshsig, _ := SignSSH(priv, GitNamespace, HashSHA512, msg)
	err := VerifySSH(pub, GitNamespace, HashSHA256, msg, sshsig)
	if err == nil {
		t.Fatal("hash-algo mismatch should reject")
	}
	if !strings.Contains(err.Error(), "hash_algorithm") {
		t.Errorf("error should mention hash_algorithm: %v", err)
	}
}

// TestSSHSIG_UnsupportedHashReportsCleanly — a bad algo string on sign
// is caught before producing garbage; on verify it's a clear error.
func TestSSHSIG_UnsupportedHashReportsCleanly(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := SignSSH(priv, GitNamespace, "md5", []byte("x")); err == nil {
		t.Error("SignSSH should reject md5")
	}
}

// TestSSHSIG_MalformedPEMRejected — mangled input surfaces a clean
// error (no panic, no silent success).
func TestSSHSIG_MalformedPEMRejected(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	err := VerifySSH(pub, GitNamespace, HashSHA512, []byte("m"), "not a pem block")
	if err == nil {
		t.Error("garbage input should fail")
	}
}

// TestSSHSIG_ReservedFieldEmpty — per spec the reserved string is
// empty. Regression: if we start filling it, verifiers that strictly
// check will reject. Decode our own output and assert.
func TestSSHSIG_ReservedFieldEmpty(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshsig, _ := SignSSH(priv, GitNamespace, HashSHA512, []byte("m"))

	// Re-parse via the public helper and check the overall shape: the
	// detailed reserved-is-empty assertion lives inside parseSSHSIGBlob
	// (would fail to decode a malformed reserved). This test guards the
	// round-trip property.
	if _, err := Ed25519PubFromSSHSIG(sshsig); err != nil {
		t.Fatalf("parse own output: %v", err)
	}
}
