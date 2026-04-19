package plugins

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CRL is stado's plugin Certificate Revocation List (DESIGN §"Phase 7.6").
//
// Signed JSON at a well-known URL — fetched at `stado plugin install` and
// refreshed on each verify. Entries revoke a specific (author fingerprint,
// version, wasm sha256) triple. Airgap users can import a signed CRL
// manually; the on-disk cache lives next to the trust store.
type CRL struct {
	Version   int        `json:"version"`    // schema version; v1 is current
	IssuedAt  time.Time  `json:"issued_at"`
	Entries   []CRLEntry `json:"entries"`
	Signature string     `json:"signature"`  // base64 Ed25519 over JSON w/ Signature==""
}

// CRLEntry is one revocation record.
type CRLEntry struct {
	AuthorFingerprint string `json:"author_fpr"`
	Version           string `json:"version"`     // empty = all versions revoked
	WASMSha256        string `json:"wasm_sha256"` // empty = match any wasm
	Reason            string `json:"reason"`
}

// LoadLocal reads a cached CRL from disk. Missing file returns (nil, nil)
// — no CRL is not an error, just an advisory in the logs.
func LoadLocal(path string) (*CRL, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c CRL
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("crl: parse %s: %w", path, err)
	}
	return &c, nil
}

// SaveLocal writes a CRL to disk atomically (0600, tmp+rename).
func SaveLocal(c *CRL, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fetch lives in crl_online.go (`!airgap`) and crl_airgap.go (`airgap`).
// Online builds fetch from a signed URL and verify against issuerPubkey;
// airgap builds return ErrAirgap so callers fall back to the on-disk
// cache written by SaveLocal.

func parseAndVerify(raw []byte, issuerPubkey ed25519.PublicKey) (*CRL, error) {
	var c CRL
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	sigB64 := c.Signature
	if sigB64 == "" {
		return nil, errors.New("missing signature")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("bad base64 signature: %w", err)
	}

	// Re-serialise with empty Signature for the canonical bytes the
	// issuer signed. MarshalIndent isn't canonical in general, but we
	// pin the shape: JSON Marshal with sorted keys, then the issuer
	// re-emits the same bytes. Tests enforce this invariant.
	c.Signature = ""
	canonical, err := json.Marshal(&c)
	if err != nil {
		return nil, fmt.Errorf("re-serialise: %w", err)
	}
	if !ed25519.Verify(issuerPubkey, canonical, sig) {
		return nil, errors.New("signature verification failed")
	}
	c.Signature = sigB64
	return &c, nil
}

// Sign generates an Ed25519 signature over the CRL's canonical bytes and
// sets c.Signature. Used by whoever produces a CRL (stado maintainers in
// production; tests here).
func (c *CRL) Sign(priv ed25519.PrivateKey) error {
	saved := c.Signature
	c.Signature = ""
	canonical, err := json.Marshal(c)
	if err != nil {
		c.Signature = saved
		return err
	}
	c.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, canonical))
	return nil
}

// IsRevoked reports whether the given manifest triple is listed in c.
// Empty Version / WASMSha256 in a CRL entry act as wildcards. Returns
// the revocation reason on a match so the caller can surface it.
func (c *CRL) IsRevoked(fpr, version, wasmSha256 string) (bool, string) {
	if c == nil {
		return false, ""
	}
	for _, e := range c.Entries {
		if e.AuthorFingerprint != fpr {
			continue
		}
		if e.Version != "" && e.Version != version {
			continue
		}
		if e.WASMSha256 != "" && !equalHex(e.WASMSha256, wasmSha256) {
			continue
		}
		return true, e.Reason
	}
	return false, ""
}

// equalHex compares two hex strings case-insensitively.
func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return bytes.EqualFold([]byte(a), []byte(b))
}
