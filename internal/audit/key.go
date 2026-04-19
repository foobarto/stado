// Package audit implements stado's tamper-evident commit signing and audit
// export (PLAN.md §5).
//
// v1 uses a stado-native Ed25519 scheme: signatures are a trailer
// `Signature: ed25519:<base64>` in the commit message, verified by hashing
// the commit's tree hash + parent hashes + the message body up to (but
// excluding) the signature trailer. Compatible with `git log` (signatures
// show as text in the message); `stado audit verify` walks refs to validate.
//
// Interop with `git log --show-signature` (SSH signature format per
// `gpg.format=ssh`) lands in a follow-up — the sig bytes are the same, only
// the envelope differs.
package audit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// KeyFileName is the default filename for stado's agent signing key.
	// Placed under ${XDG_DATA_HOME}/stado/keys/.
	KeyFileName = "agent.ed25519"

	pemType = "STADO ED25519 PRIVATE KEY"
)

// LoadOrCreateKey opens an existing key file or generates a fresh one with
// 0600 permissions. The private key is PEM-encoded.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if key, err := loadKey(path); err == nil {
		return key, nil
	}
	return createKey(path)
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("audit: key PEM malformed")
	}
	if block.Type != pemType {
		return nil, fmt.Errorf("audit: unexpected PEM type %q", block.Type)
	}
	if len(block.Bytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("audit: key size %d, want %d", len(block.Bytes), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(block.Bytes), nil
}

func createKey(path string) (ed25519.PrivateKey, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit: mkdir key dir: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("audit: generate key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: priv})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("audit: write key: %w", err)
	}
	return priv, nil
}

// Fingerprint returns a short, stable identifier for a public key — the
// first 16 hex chars of sha256(pub). Useful for UIs and trust pins.
func Fingerprint(pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ""
	}
	return FingerprintBytes(pub)
}
