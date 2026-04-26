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
	"io"
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
	} else if exists, statErr := keyPathExists(path); statErr != nil {
		return nil, statErr
	} else if exists {
		return nil, err
	}
	return createKey(path)
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- audit key path is derived from stado config state.
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
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("audit: generate key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: priv})
	if err := WritePrivateKeyFile(path, pemBytes); err != nil {
		return nil, fmt.Errorf("audit: write key: %w", err)
	}
	return priv, nil
}

func keyPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// WritePrivateKeyFile creates a new private-key file with 0600 permissions
// without following or overwriting an existing final path.
func WritePrivateKeyFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir key dir: %w", err)
	}
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return fmt.Errorf("invalid key path: %s", path)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private key file is a symlink: %s", path)
		}
		return fmt.Errorf("private key file already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Fingerprint returns a short, stable identifier for a public key — the
// first 16 hex chars of sha256(pub). Useful for UIs and trust pins.
func Fingerprint(pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ""
	}
	return FingerprintBytes(pub)
}
