package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// TrailerKey is the commit-message trailer holding the Ed25519 signature.
const TrailerKey = "Signature"

// Prefix for the signature value (future-proofs scheme switches).
const SigPrefix = "ed25519:"

// Signer builds and appends a signature trailer to a commit-message body.
// The signature covers: the message body (with any existing trailing empty
// lines trimmed, and any preexisting Signature trailer stripped) + the
// serialized tree + parent hashes in the caller-provided digest form.
type Signer struct {
	priv ed25519.PrivateKey
}

func NewSigner(priv ed25519.PrivateKey) *Signer { return &Signer{priv: priv} }

// Public returns the public half of the signing key.
func (s *Signer) Public() ed25519.PublicKey {
	if s == nil || s.priv == nil {
		return nil
	}
	return s.priv.Public().(ed25519.PublicKey)
}

// Sign returns the Ed25519 signature over the canonical bytes (see
// CanonicalBytes). Returns nil signature if the Signer itself is nil, so
// call-sites can treat signing as optional.
func (s *Signer) Sign(treeHash string, parents []string, body string) (sigB64 string) {
	if s == nil || s.priv == nil {
		return ""
	}
	sig := ed25519.Sign(s.priv, CanonicalBytes(treeHash, parents, body))
	return SigPrefix + base64.StdEncoding.EncodeToString(sig)
}

// AppendTrailer inserts or replaces the Signature trailer in a commit body.
// Body should end with a newline; the trailer is added as the last line.
func AppendTrailer(body, sigValue string) string {
	body = StripSignatureTrailer(body)
	body = strings.TrimRight(body, "\n") + "\n"
	return body + TrailerKey + ": " + sigValue + "\n"
}

// sigTrailerRE matches a single-line `Signature: ed25519:<base64>` trailer.
var sigTrailerRE = regexp.MustCompile(`(?m)^Signature:\s*ed25519:[A-Za-z0-9+/=]+\s*$\n?`)

// StripSignatureTrailer removes the Signature trailer from a commit message
// body (if present). Used to reconstruct the pre-signature bytes during both
// signing (strip any pre-existing sig) and verification.
func StripSignatureTrailer(body string) string { return sigTrailerRE.ReplaceAllString(body, "") }

// CanonicalBytes returns the exact bytes the Signer signs: a framed form
// that pins the commit's tree hash and parent set alongside the body.
// Changing any of the three invalidates the signature.
func CanonicalBytes(treeHash string, parents []string, body string) []byte {
	body = StripSignatureTrailer(body)
	body = strings.TrimRight(body, "\n")
	var b strings.Builder
	b.WriteString("stado-audit-v1\n")
	b.WriteString("tree ")
	b.WriteString(treeHash)
	b.WriteByte('\n')
	for _, p := range parents {
		b.WriteString("parent ")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(body)
	b.WriteByte('\n')
	return []byte(b.String())
}

// ExtractSignature returns the base64 signature from a commit body, or
// ("", nil) when none is present.
func ExtractSignature(body string) (sigB64 string, ok bool) {
	m := sigTrailerRE.FindStringSubmatch(body)
	if len(m) == 0 {
		return "", false
	}
	line := strings.TrimSpace(m[0])
	_, _, val := cutTrailer(line)
	return strings.TrimPrefix(val, SigPrefix), true
}

func cutTrailer(line string) (key, sep, val string) {
	if i := strings.Index(line, ":"); i > 0 {
		return line[:i], ":", strings.TrimSpace(line[i+1:])
	}
	return "", "", line
}

// Verify checks the signature trailer against the given pubkey. Returns nil
// on successful verification.
func Verify(pub ed25519.PublicKey, treeHash string, parents []string, body string) error {
	sigB64, ok := ExtractSignature(body)
	if !ok {
		return errors.New("audit: no signature trailer")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errors.New("audit: signature not base64")
	}
	if !ed25519.Verify(pub, CanonicalBytes(treeHash, parents, body), sig) {
		return errors.New("audit: signature invalid")
	}
	return nil
}

// FingerprintBytes → sha256[:8] hex-encoded. Exposed at the top-level for
// callers who only have a pub key slice.
func FingerprintBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
