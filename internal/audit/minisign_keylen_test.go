package audit

import (
	"crypto/ed25519"
	"testing"
)

// Hardening: ed25519.Verify panics on a wrong-length key. MinisignVerify must
// return an error for a malformed public key, never crash the process.
func TestMinisignVerify_RejectsWrongLengthKey(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MinisignVerify panicked on a wrong-length key (should return an error): %v", r)
		}
	}()
	for _, n := range []int{0, 16, ed25519.PublicKeySize - 1, ed25519.PublicKeySize + 1} {
		if _, err := MinisignVerify(make([]byte, n), []byte("message"), []byte("untrusted comment: x\nAAAA\ntrusted comment: y\nBBBB\n")); err == nil {
			t.Errorf("len(pub)=%d: expected an error, got nil", n)
		}
	}
}
