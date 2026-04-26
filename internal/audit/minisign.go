package audit

// Minisign-compatible signing for stado release artifacts (PLAN §10.3).
//
// v1 uses prehashed Ed25519 ("ED" sig-algorithm ID) — the recommended mode
// for large files since it hashes with BLAKE2b-512 once, then signs the
// 64-byte digest. File layout:
//
//	untrusted comment: signature from minisign secret key
//	<base64: "ED" (2) + key_id (8) + sig (64)>
//	trusted comment: <anything, rendered by `minisign -V` on success>
//	<base64: Ed25519 sig over (sig_bytes || trusted_comment_bytes)>
//
// The stado agent signing key (Phase 5) and the minisign release key are
// distinct — one is per-host, the other is a long-lived offline key. This
// file implements the file format so stado can both produce and verify
// minisign-format signatures wherever we need to.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"golang.org/x/crypto/blake2b"
)

const (
	// sigAlgPrehashed is the minisign signature-algorithm identifier for
	// BLAKE2b-prehashed Ed25519. (Non-prehashed is "Ed".)
	sigAlgPrehashed = "ED"

	minisignSigSize = 2 + 8 + 64 // alg + key_id + raw ed25519 sig
)

// MinisignSign produces a .minisig file contents for `message` signed by
// priv, tagged with key_id. trustedComment is the line that appears to the
// user when `minisign -V` reports success.
func MinisignSign(priv ed25519.PrivateKey, keyID uint64, message []byte, untrustedComment, trustedComment string) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("minisign: wrong key size")
	}
	if untrustedComment == "" {
		untrustedComment = "signature from stado"
	}
	if trustedComment == "" {
		trustedComment = "verified by stado"
	}

	// Prehashed signature body: alg("ED") + key_id + Ed25519(BLAKE2b(msg)).
	hash := blake2b.Sum512(message)
	sig := ed25519.Sign(priv, hash[:])

	body := make([]byte, 0, minisignSigSize)
	body = append(body, []byte(sigAlgPrehashed)...)
	keyIDBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(keyIDBytes, keyID)
	body = append(body, keyIDBytes...)
	body = append(body, sig...)

	// Global signature: Ed25519(priv, raw_sig || trusted_comment).
	globalMsg := append([]byte(nil), sig...)
	globalMsg = append(globalMsg, []byte(trustedComment)...)
	globalSig := ed25519.Sign(priv, globalMsg)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "untrusted comment: %s\n", untrustedComment)
	buf.WriteString(base64.StdEncoding.EncodeToString(body))
	buf.WriteByte('\n')
	fmt.Fprintf(&buf, "trusted comment: %s\n", trustedComment)
	buf.WriteString(base64.StdEncoding.EncodeToString(globalSig))
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// MinisignVerify validates a .minisig file against pub + message. Returns
// the trusted comment on success.
func MinisignVerify(pub ed25519.PublicKey, message, sigFile []byte) (trusted string, err error) {
	untrustedLine, sigLine, trustedLine, globalLine, err := parseMinisignFile(sigFile)
	if err != nil {
		return "", err
	}
	_ = untrustedLine

	body, err := base64.StdEncoding.DecodeString(sigLine)
	if err != nil {
		return "", fmt.Errorf("minisign: decode body: %w", err)
	}
	if len(body) != minisignSigSize {
		return "", fmt.Errorf("minisign: signature body = %d bytes, want %d", len(body), minisignSigSize)
	}
	alg := string(body[:2])
	if alg != sigAlgPrehashed {
		return "", fmt.Errorf("minisign: unsupported alg %q (only %q)", alg, sigAlgPrehashed)
	}
	sig := body[10:] // skip alg + key_id

	// Verify the main sig over BLAKE2b(message).
	hash := blake2b.Sum512(message)
	if !ed25519.Verify(pub, hash[:], sig) {
		return "", errors.New("minisign: signature invalid")
	}

	// Verify the global sig over sig || trusted_comment.
	globalSig, err := base64.StdEncoding.DecodeString(globalLine)
	if err != nil {
		return "", fmt.Errorf("minisign: decode global sig: %w", err)
	}
	globalMsg := append([]byte(nil), sig...)
	globalMsg = append(globalMsg, []byte(trustedLine)...)
	if !ed25519.Verify(pub, globalMsg, globalSig) {
		return "", errors.New("minisign: trusted comment signature invalid")
	}
	return trustedLine, nil
}

// parseMinisignFile extracts the four lines we care about. Tolerates CRLF
// and trailing newlines; case-insensitive comment key matching.
func parseMinisignFile(b []byte) (untrusted, sig, trusted, global string, err error) {
	lines := strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n")
	var clean []string
	for _, l := range lines {
		if l = strings.TrimRight(l, " \t"); l != "" {
			clean = append(clean, l)
		}
	}
	if len(clean) < 4 {
		return "", "", "", "", fmt.Errorf("minisign: expected 4 non-empty lines, got %d", len(clean))
	}
	u, ok := trimPrefixI(clean[0], "untrusted comment:")
	if !ok {
		return "", "", "", "", errors.New("minisign: first line must start with 'untrusted comment:'")
	}
	t, ok := trimPrefixI(clean[2], "trusted comment:")
	if !ok {
		return "", "", "", "", errors.New("minisign: third line must start with 'trusted comment:'")
	}
	return strings.TrimSpace(u), clean[1], strings.TrimSpace(t), clean[3], nil
}

func trimPrefixI(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return s, false
	}
	if !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// MinisignSignFile is the convenient path-based version of MinisignSign.
// Reads `path`, signs the contents, and writes `path.minisig`.
func MinisignSignFile(priv ed25519.PrivateKey, keyID uint64, path, untrustedComment, trustedComment string) error {
	f, err := os.Open(path) // #nosec G304 -- caller supplies the artifact path to sign.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	sig, err := MinisignSign(priv, keyID, body, untrustedComment, trustedComment)
	if err != nil {
		return err
	}
	return writeShareableSidecar(path+".minisig", sig)
}

func writeShareableSidecar(path string, data []byte) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid sidecar path: %s", path)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return workdirpath.WriteRootFileAtomic(root, name, data, 0o644)
}
