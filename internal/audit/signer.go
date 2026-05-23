package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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

// Sign returns the Ed25519 signature over the v1 canonical bytes (see
// [CanonicalBytes]). Returns "" if the Signer itself is nil so
// call-sites can treat signing as optional. Kept at the v1 form
// to honor the [CommitSigner] interface contract pre-#138; new
// production sites should call [SignV2] via the [CommitSignerV2]
// extension interface so the author / committer / timestamps are
// bound too.
//
// Verify accepts both forms — v2 first, v1 fallback — so existing
// audit history signed with this method still verifies.
func (s *Signer) Sign(treeHash string, parents []string, body string) (sigB64 string) {
	if s == nil || s.priv == nil {
		return ""
	}
	sig := ed25519.Sign(s.priv, CanonicalBytes(treeHash, parents, body))
	return SigPrefix + base64.StdEncoding.EncodeToString(sig)
}

// SignV2 returns the Ed25519 signature over the v2 canonical bytes
// (see [CanonicalBytesV2]) — extends v1 by binding the author and
// committer identity + unix timestamps into the signed payload.
// Codex #138: pre-fix v1 omitted these, letting an attacker with
// sidecar write rewrite the author or backdate while the signature
// still verified.
//
// Takes primitives so the matching interface in state/git
// ([CommitSignerV2]) doesn't have to import audit (would be a
// cycle). The args correspond exactly to a [SignedIdentity] struct
// — convenience wrapper for callers that already have one is
// trivial.
//
// New production sites use SignV2; the legacy [Sign] is kept so
// the [CommitSigner] interface contract holds for existing callers.
func (s *Signer) SignV2(treeHash string, parents []string, body string,
	authorName, authorEmail string, authorUnix int64,
	committerName, committerEmail string, committerUnix int64,
) (sigB64 string) {
	if s == nil || s.priv == nil {
		return ""
	}
	ident := SignedIdentity{
		AuthorName: authorName, AuthorEmail: authorEmail, AuthorUnix: authorUnix,
		CommitterName: committerName, CommitterEmail: committerEmail, CommitterUnix: committerUnix,
	}
	sig := ed25519.Sign(s.priv, CanonicalBytesV2(treeHash, parents, body, ident))
	return SigPrefix + base64.StdEncoding.EncodeToString(sig)
}

// SignSSH produces an SSHSIG-format signature over `message` bound to
// the `git` namespace and sha512 hash (git tooling interop).
// Returns "" when the Signer is nil so call-sites stay straight-line.
//
// `message` should be the git commit canonical bytes (i.e. the commit
// object encoded *without* the gpgsig header) so `git log
// --show-signature` / `ssh-keygen -Y verify` can verify against the
// signer's public key.
func (s *Signer) SignSSH(message []byte) (string, error) {
	if s == nil || s.priv == nil {
		return "", nil
	}
	return SignSSH(s.priv, GitNamespace, HashSHA512, message)
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

// SignedIdentity carries the author + committer info that becomes
// part of the v2 audit-signature payload. Codex #138: v1 covered
// only tree+parents+body, so an attacker with sidecar write could
// rewrite the author identity or backdate timestamps via
// `git filter-branch` / `git replace --graft` / direct object surgery
// and the existing signature on the (unchanged) tree+parents+body
// still verified — tamper-evidence broke silently. v2 binds the
// identity fields too.
//
// Times are unix seconds (matches git's epoch-format committer time)
// rather than RFC3339 to keep the canonical bytes stable across
// timezone display conventions.
type SignedIdentity struct {
	AuthorName     string
	AuthorEmail    string
	AuthorUnix     int64
	CommitterName  string
	CommitterEmail string
	CommitterUnix  int64
}

// CanonicalBytes returns the v1 audit-signature payload. Kept for
// backward-compat verification of signatures produced before #138.
// New signatures use [CanonicalBytesV2]; [Verify] tries v2 first and
// falls back to v1 so pre-fix audit history still verifies.
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

// CanonicalBytesV2 returns the v2 audit-signature payload. Extends v1
// with `author` + `committer` lines so the author/committer name +
// email + unix timestamp are cryptographically bound. The framing
// string is `stado-audit-v2\n`. The author/committer lines mirror
// git's own commit-object format for parser familiarity:
//
//	author <name> <email> <unix-time>
//	committer <name> <email> <unix-time>
//
// Changing any of {tree, parents, author identity, committer identity,
// author time, committer time, body} invalidates the v2 signature.
func CanonicalBytesV2(treeHash string, parents []string, body string, ident SignedIdentity) []byte {
	body = StripSignatureTrailer(body)
	body = strings.TrimRight(body, "\n")
	var b strings.Builder
	b.WriteString("stado-audit-v2\n")
	b.WriteString("tree ")
	b.WriteString(treeHash)
	b.WriteByte('\n')
	for _, p := range parents {
		b.WriteString("parent ")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "author %s <%s> %d\n", ident.AuthorName, ident.AuthorEmail, ident.AuthorUnix)
	fmt.Fprintf(&b, "committer %s <%s> %d\n", ident.CommitterName, ident.CommitterEmail, ident.CommitterUnix)
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

// Verify checks the v1 signature trailer against the given pubkey.
// Kept for backward-compat callers; new sites should use [VerifyV2]
// so the v2 author/committer/timestamps binding is checked too. This
// v1 form only verifies tree+parents+body — a v1 signature on a
// commit whose author/timestamps have been tampered still appears
// valid here.
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

// VerifyV2 checks the signature trailer against the given pubkey,
// trying v2 (author/committer/timestamps bound — see
// [CanonicalBytesV2]) first and falling back to v1 (tree+parents+body
// only — see [CanonicalBytes]) so pre-#138 audit history still
// verifies after the scheme bump. Returns a sentinel error wrapping
// the underlying ed25519 failure on mismatch.
//
// Callers that have access to the commit's author/committer info
// SHOULD use VerifyV2 (`audit verify` does). Callers that don't
// (older code, third-party verifiers) can still call [Verify] which
// only checks v1.
func VerifyV2(pub ed25519.PublicKey, treeHash string, parents []string, body string, ident SignedIdentity) error {
	sigB64, ok := ExtractSignature(body)
	if !ok {
		return errors.New("audit: no signature trailer")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errors.New("audit: signature not base64")
	}
	if ed25519.Verify(pub, CanonicalBytesV2(treeHash, parents, body, ident), sig) {
		return nil
	}
	// v1 fallback for sigs produced before #138 — pre-fix history
	// still verifies, even though the v1 payload doesn't bind the
	// author/committer/timestamps.
	if ed25519.Verify(pub, CanonicalBytes(treeHash, parents, body), sig) {
		return nil
	}
	return errors.New("audit: signature invalid")
}

// FingerprintBytes → sha256[:8] hex-encoded. Exposed at the top-level for
// callers who only have a pub key slice.
func FingerprintBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
