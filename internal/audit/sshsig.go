package audit

// SSH signature (SSHSIG) format for Ed25519 keys — interop with
// `git log --show-signature` and other git tooling that recognises
// gpgsig headers in the SSH-signature PEM envelope.
//
// Spec: https://github.com/openssh/openssh-portable/blob/master/PROTOCOL.sshsig
//
// Layout of the binary blob (wrapped in `-----BEGIN SSH SIGNATURE-----`):
//
//     byte[6]  "SSHSIG"
//     uint32   version            (1)
//     string   publickey          (SSH wire format — "ssh-ed25519" + raw pubkey)
//     string   namespace          ("git" for git commits)
//     string   reserved           ("")
//     string   hash_algorithm     ("sha512")
//     string   signature          (SSH wire format — "ssh-ed25519" + raw sig)
//
// The signature covers a framed form of (namespace, hash_algorithm,
// H(message)) — see `sshsigSignedBlob`.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
)

const (
	sshsigMagic          = "SSHSIG"
	sshsigVersion        = uint32(1)
	sshsigKeyTypeEd25519 = "ssh-ed25519"
	sshsigPEMBlockType   = "SSH SIGNATURE"
	// GitNamespace is the namespace string git uses for its commit
	// signatures. Matches `git config gpg.ssh.defaultKeyCommand`
	// behaviour.
	GitNamespace = "git"
	// HashSHA512 is the only hash the Git tooling accepts for SSH
	// signature verification in practice; exported so callers don't
	// hard-code the string.
	HashSHA512 = "sha512"
	// HashSHA256 is listed in the SSHSIG spec and supported here for
	// symmetry, but git's ssh-keygen verify rejects it — use
	// HashSHA512 for Git-compatible signatures.
	HashSHA256 = "sha256"
)

// SignSSH builds an SSHSIG-format signature over `message` using the
// provided Ed25519 private key. Returns the PEM-wrapped blob ready to
// drop into a git `gpgsig` header (or to feed to `ssh-keygen -Y
// verify`).
//
// `namespace` is the SSHSIG namespace string — use `GitNamespace`
// ("git") for commit signing. `hashAlgo` must be "sha512" for Git
// interop.
func SignSSH(priv ed25519.PrivateKey, namespace, hashAlgo string, message []byte) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("sshsig: private key length %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	if namespace == "" {
		return "", errors.New("sshsig: namespace required")
	}
	hash, err := hashMessage(message, hashAlgo)
	if err != nil {
		return "", err
	}
	signed := sshsigSignedBlob(namespace, hashAlgo, hash)
	sig := ed25519.Sign(priv, signed)

	pubKey := priv.Public().(ed25519.PublicKey)
	blob := sshsigBlob(pubKey, namespace, hashAlgo, sig)
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  sshsigPEMBlockType,
		Bytes: blob,
	})), nil
}

// VerifySSH verifies an SSHSIG-format signature over `message` against
// `pub`. Returns nil on success; an error for every other case.
// `namespace` and `hashAlgo` must match what the signer used — passing
// the wrong ones is a verification failure, not a programmer error,
// because a mismatched envelope is indistinguishable from a bad
// signature from the caller's point of view.
func VerifySSH(pub ed25519.PublicKey, namespace, hashAlgo string, message []byte, sshsigPEM string) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("sshsig: public key length %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	block, _ := pem.Decode([]byte(sshsigPEM))
	if block == nil || block.Type != sshsigPEMBlockType {
		return errors.New("sshsig: not an SSH SIGNATURE PEM block")
	}
	gotPub, gotNS, gotHashAlgo, gotSig, err := parseSSHSIGBlob(block.Bytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotPub, pub) {
		return errors.New("sshsig: embedded pubkey does not match expected signer")
	}
	if gotNS != namespace {
		return fmt.Errorf("sshsig: namespace = %q, want %q", gotNS, namespace)
	}
	if gotHashAlgo != hashAlgo {
		return fmt.Errorf("sshsig: hash_algorithm = %q, want %q", gotHashAlgo, hashAlgo)
	}
	hash, err := hashMessage(message, hashAlgo)
	if err != nil {
		return err
	}
	signed := sshsigSignedBlob(namespace, hashAlgo, hash)
	if !ed25519.Verify(pub, signed, gotSig) {
		return errors.New("sshsig: signature invalid")
	}
	return nil
}

// hashMessage applies the caller-chosen hash algorithm.
func hashMessage(message []byte, algo string) ([]byte, error) {
	switch algo {
	case HashSHA256:
		h := sha256.Sum256(message)
		return h[:], nil
	case HashSHA512:
		h := sha512.Sum512(message)
		return h[:], nil
	default:
		return nil, fmt.Errorf("sshsig: unsupported hash algorithm %q (want %q or %q)",
			algo, HashSHA256, HashSHA512)
	}
}

// sshsigSignedBlob serialises the tuple the signer actually signs over:
// MAGIC + namespace + reserved + hash_algorithm + H(message), each as
// an SSH length-prefixed string after the MAGIC preamble.
func sshsigSignedBlob(namespace, hashAlgo string, hashed []byte) []byte {
	var b bytes.Buffer
	b.WriteString(sshsigMagic)
	writeSSHString(&b, []byte(namespace))
	writeSSHString(&b, nil) // reserved
	writeSSHString(&b, []byte(hashAlgo))
	writeSSHString(&b, hashed)
	return b.Bytes()
}

// sshsigBlob builds the outer SSHSIG binary (prior to PEM wrapping).
func sshsigBlob(pub ed25519.PublicKey, namespace, hashAlgo string, sig []byte) []byte {
	var b bytes.Buffer
	b.WriteString(sshsigMagic)
	_ = binary.Write(&b, binary.BigEndian, sshsigVersion)
	writeSSHString(&b, sshEd25519Pubkey(pub))
	writeSSHString(&b, []byte(namespace))
	writeSSHString(&b, nil) // reserved
	writeSSHString(&b, []byte(hashAlgo))
	writeSSHString(&b, sshEd25519Signature(sig))
	return b.Bytes()
}

// sshEd25519Pubkey encodes an Ed25519 public key in SSH wire format:
// string("ssh-ed25519") + string(32-byte raw key).
func sshEd25519Pubkey(pub ed25519.PublicKey) []byte {
	var b bytes.Buffer
	writeSSHString(&b, []byte(sshsigKeyTypeEd25519))
	writeSSHString(&b, pub)
	return b.Bytes()
}

// sshEd25519Signature encodes an Ed25519 signature in SSH wire format:
// string("ssh-ed25519") + string(64-byte raw signature).
func sshEd25519Signature(sig []byte) []byte {
	var b bytes.Buffer
	writeSSHString(&b, []byte(sshsigKeyTypeEd25519))
	writeSSHString(&b, sig)
	return b.Bytes()
}

// writeSSHString emits an SSH length-prefixed string: u32 big-endian
// length followed by the raw bytes. Nil/empty slice encodes as length 0.
func writeSSHString(w *bytes.Buffer, s []byte) {
	_ = binary.Write(w, binary.BigEndian, uint32(len(s)))
	w.Write(s)
}

// parseSSHSIGBlob inverts sshsigBlob — used during verification.
// Returns (pubkey, namespace, hash_algorithm, signature).
func parseSSHSIGBlob(raw []byte) (ed25519.PublicKey, string, string, []byte, error) {
	r := bytes.NewReader(raw)
	magic := make([]byte, len(sshsigMagic))
	if _, err := r.Read(magic); err != nil || string(magic) != sshsigMagic {
		return nil, "", "", nil, fmt.Errorf("sshsig: bad magic (got %q)", magic)
	}
	var ver uint32
	if err := binary.Read(r, binary.BigEndian, &ver); err != nil {
		return nil, "", "", nil, fmt.Errorf("sshsig: read version: %w", err)
	}
	if ver != sshsigVersion {
		return nil, "", "", nil, fmt.Errorf("sshsig: version %d unsupported", ver)
	}
	pubBlob, err := readSSHString(r)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("sshsig: pubkey: %w", err)
	}
	pub, err := parseSSHEd25519Pubkey(pubBlob)
	if err != nil {
		return nil, "", "", nil, err
	}
	ns, err := readSSHString(r)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("sshsig: namespace: %w", err)
	}
	if _, err := readSSHString(r); err != nil { // reserved
		return nil, "", "", nil, fmt.Errorf("sshsig: reserved: %w", err)
	}
	hashAlgo, err := readSSHString(r)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("sshsig: hash_algorithm: %w", err)
	}
	sigBlob, err := readSSHString(r)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("sshsig: signature blob: %w", err)
	}
	sig, err := parseSSHEd25519Signature(sigBlob)
	if err != nil {
		return nil, "", "", nil, err
	}
	return pub, string(ns), string(hashAlgo), sig, nil
}

func parseSSHEd25519Pubkey(blob []byte) (ed25519.PublicKey, error) {
	r := bytes.NewReader(blob)
	typeName, err := readSSHString(r)
	if err != nil || string(typeName) != sshsigKeyTypeEd25519 {
		return nil, fmt.Errorf("sshsig: pubkey type = %q, want %q", typeName, sshsigKeyTypeEd25519)
	}
	key, err := readSSHString(r)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("sshsig: pubkey length %d, want %d", len(key), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(key), nil
}

func parseSSHEd25519Signature(blob []byte) ([]byte, error) {
	r := bytes.NewReader(blob)
	typeName, err := readSSHString(r)
	if err != nil || string(typeName) != sshsigKeyTypeEd25519 {
		return nil, fmt.Errorf("sshsig: signature type = %q, want %q", typeName, sshsigKeyTypeEd25519)
	}
	sig, err := readSSHString(r)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("sshsig: signature length %d, want %d", len(sig), ed25519.SignatureSize)
	}
	return sig, nil
}

// readSSHString reads a u32-length-prefixed byte string from r.
func readSSHString(r *bytes.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	if int64(n) > int64(r.Len()) {
		return nil, fmt.Errorf("sshsig: length %d exceeds remaining %d", n, r.Len())
	}
	out := make([]byte, n)
	if _, err := r.Read(out); err != nil && n > 0 {
		return nil, err
	}
	return out, nil
}

// Ed25519PubFromSSHSIG extracts the signer's ed25519 pubkey from a
// PEM-wrapped SSHSIG blob — exposed so callers can display the key
// fingerprint without re-verifying (e.g. "who signed this commit?"
// command-line tools).
func Ed25519PubFromSSHSIG(sshsigPEM string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(sshsigPEM))
	if block == nil || block.Type != sshsigPEMBlockType {
		return nil, errors.New("sshsig: not an SSH SIGNATURE PEM block")
	}
	pub, _, _, _, err := parseSSHSIGBlob(block.Bytes)
	return pub, err
}

