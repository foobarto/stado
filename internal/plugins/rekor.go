package plugins

// Rekor transparency-log integration for plugin manifests (PLAN §7.7).
//
// We talk to Rekor's REST API directly — no sigstore Go deps (those
// pull in ~50 transitive modules + OIDC / Fulcio we don't use). The
// hashedrekord v0.0.1 format is enough: a (sha256 hash, signature,
// PEM-encoded pubkey) triple that can round-trip through any Rekor
// instance (sigstore.dev, a self-hosted one, a corp internal log).
//
// Online entry points (Upload + SearchByHash) live in rekor_online.go
// with `//go:build !airgap`; the airgap counterpart returns ErrRekorAirgap.

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

// RekorEntry is the subset of a Rekor log entry we care about: the raw
// hashedrekord body (base64-encoded JSON) and the log-index assigned
// to it. The full API response has more fields (verification proofs,
// integration timestamps); we parse only what we need to verify.
type RekorEntry struct {
	UUID     string
	LogIndex int64
	// Body is the base64-encoded canonical hashedrekord JSON Rekor
	// returns — decoded into HashedRekord by parseRekorBody.
	Body string
}

// HashedRekord is the v0.0.1 ProposedEntry schema. Mirrors Rekor's
// own types/hashedrekord/v0.0.1/entry.go but kept tiny and local —
// we only need the signature / pubkey / hash triple.
type HashedRekord struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Spec       HashedRekordSpec `json:"spec"`
}

// HashedRekordSpec is the entry payload.
type HashedRekordSpec struct {
	Signature HashedRekordSig  `json:"signature"`
	Data      HashedRekordData `json:"data"`
}

// HashedRekordSig wraps the raw signature + signer pubkey. Both are
// base64 in the wire format.
type HashedRekordSig struct {
	Content   string             `json:"content"`
	PublicKey HashedRekordPubkey `json:"publicKey"`
}

// HashedRekordPubkey carries the signer's public key as a
// base64-encoded PEM blob (SubjectPublicKeyInfo form for ed25519).
type HashedRekordPubkey struct {
	Content string `json:"content"`
}

// HashedRekordData pins the digest of the signed artefact.
type HashedRekordData struct {
	Hash HashedRekordHash `json:"hash"`
}

// HashedRekordHash is {algorithm: "sha256", value: "<hex>"}.
type HashedRekordHash struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// NewHashedRekord constructs a hashedrekord entry for the given manifest
// signature + signer. `manifestSHA256` is the raw 32-byte digest of the
// manifest canonical bytes (same digest that Rekor re-derives from the
// pubkey + sig during verification on its side).
func NewHashedRekord(manifestSHA256, sig []byte, pub ed25519.PublicKey) (HashedRekord, error) {
	if len(manifestSHA256) != 32 {
		return HashedRekord{}, fmt.Errorf("rekor: sha256 must be 32 bytes, got %d", len(manifestSHA256))
	}
	pemPub, err := ed25519PubPEM(pub)
	if err != nil {
		return HashedRekord{}, err
	}
	return HashedRekord{
		APIVersion: "0.0.1",
		Kind:       "hashedrekord",
		Spec: HashedRekordSpec{
			Signature: HashedRekordSig{
				Content: base64.StdEncoding.EncodeToString(sig),
				PublicKey: HashedRekordPubkey{
					Content: base64.StdEncoding.EncodeToString(pemPub),
				},
			},
			Data: HashedRekordData{
				Hash: HashedRekordHash{
					Algorithm: "sha256",
					Value:     hex.EncodeToString(manifestSHA256),
				},
			},
		},
	}, nil
}

// ed25519PubPEM marshals an Ed25519 public key into its PKIX /
// SubjectPublicKeyInfo PEM encoding — the shape Rekor expects.
func ed25519PubPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("rekor: marshal pubkey: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// parseHashedRekordBody decodes Rekor's base64-encoded body JSON into
// a HashedRekord. Rekor re-canonicalises what we send so byte-level
// equality isn't guaranteed — we compare on semantic fields.
func parseHashedRekordBody(body string) (HashedRekord, error) {
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return HashedRekord{}, fmt.Errorf("rekor: decode body: %w", err)
	}
	var h HashedRekord
	if err := json.Unmarshal(raw, &h); err != nil {
		return HashedRekord{}, fmt.Errorf("rekor: parse body: %w", err)
	}
	return h, nil
}

// ErrRekorNotFound is returned when a search yielded no entries — e.g.
// the plugin was never published to the configured log.
var ErrRekorNotFound = errors.New("rekor: no entry for this signature")

// VerifyEntry asserts that entry's body has the expected signature,
// pubkey, and hash. Used by callers who fetched an entry by UUID and
// want to confirm it's the one they're looking for.
func VerifyEntry(entry RekorEntry, wantSig []byte, wantPub ed25519.PublicKey, wantSHA256 []byte) error {
	body, err := parseHashedRekordBody(entry.Body)
	if err != nil {
		return err
	}
	gotSig, err := base64.StdEncoding.DecodeString(body.Spec.Signature.Content)
	if err != nil {
		return fmt.Errorf("rekor: decode entry sig: %w", err)
	}
	if !bytesEqual(gotSig, wantSig) {
		return errors.New("rekor: entry signature mismatch")
	}
	gotPubPEM, err := base64.StdEncoding.DecodeString(body.Spec.Signature.PublicKey.Content)
	if err != nil {
		return fmt.Errorf("rekor: decode entry pubkey: %w", err)
	}
	block, _ := pem.Decode(gotPubPEM)
	if block == nil {
		return errors.New("rekor: entry pubkey not PEM-encoded")
	}
	anyKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("rekor: parse entry pubkey: %w", err)
	}
	edPub, ok := anyKey.(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("rekor: entry pubkey not ed25519 (got %T)", anyKey)
	}
	if !bytesEqual(edPub, wantPub) {
		return errors.New("rekor: entry pubkey mismatch")
	}
	if body.Spec.Data.Hash.Algorithm != "sha256" {
		return fmt.Errorf("rekor: unexpected hash algo %q", body.Spec.Data.Hash.Algorithm)
	}
	wantHex := hex.EncodeToString(wantSHA256)
	if body.Spec.Data.Hash.Value != wantHex {
		return errors.New("rekor: entry hash mismatch")
	}
	return nil
}

// bytesEqual is crypto-free here because we're comparing public data
// (pubkeys + signatures that either side can generate). The
// constant-time primitive is not required.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
