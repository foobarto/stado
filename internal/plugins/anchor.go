package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxAnchorPubkeyBytes = 4 << 10
	anchorHTTPTimeout    = 15 * time.Second
)

// FetchAnchorPubkey fetches the author pubkey from the owner's well-known
// anchor URL and returns the hex-encoded pubkey string. EP-0039 §C.
func FetchAnchorPubkey(url string) (string, error) {
	cl := &http.Client{Timeout: anchorHTTPTimeout}
	resp, err := cl.Get(url) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("anchor fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", fmt.Errorf("anchor not found at %s — owner may not publish a stado-plugins anchor repo", url)
	case http.StatusOK:
		// ok
	default:
		return "", fmt.Errorf("anchor fetch %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnchorPubkeyBytes))
	if err != nil {
		return "", fmt.Errorf("anchor read %s: %w", url, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// AnchorTrustScope controls how broadly a trusted fingerprint applies.
type AnchorTrustScope string

const (
	// AnchorTrustOwner trusts all repos by this owner (most common).
	AnchorTrustOwner AnchorTrustScope = "owner"
	// AnchorTrustRepo trusts only the specific repo.
	AnchorTrustRepo AnchorTrustScope = "repo"
)

// AnchorTrustEntry is one owner-scoped trust record. Keyed by OwnerKey
// (host/owner), not per-fingerprint like the existing TrustStore.
type AnchorTrustEntry struct {
	OwnerKey    string           `json:"owner_key"`
	Fingerprint string           `json:"fingerprint"`
	Scope       AnchorTrustScope `json:"scope"`
	TrustedAt   string           `json:"trusted_at"`
}

// AnchorTrustStore is a persistent per-user store of owner anchor trust.
// Stored as one JSON file per owner under dir/<safe-owner-key>.json.
// Separate from the existing per-key TrustStore.
type AnchorTrustStore struct {
	dir string
}

// NewAnchorTrustStore returns a store rooted at dir/anchor-trust/.
func NewAnchorTrustStore(stateDir string) *AnchorTrustStore {
	return &AnchorTrustStore{dir: filepath.Join(stateDir, "plugins", "anchor-trust")}
}

// IsTrusted returns true when the given owner+fingerprint combination is
// in the trust store.
func (s *AnchorTrustStore) IsTrusted(ownerKey, fingerprint string) (bool, error) {
	entry, err := s.load(ownerKey)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return entry.Fingerprint == fingerprint, nil
}

// Trust stores a new entry for ownerKey+fingerprint (TOFU — first caller wins).
func (s *AnchorTrustStore) Trust(ownerKey, fingerprint string, scope AnchorTrustScope) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	entry := AnchorTrustEntry{
		OwnerKey:    ownerKey,
		Fingerprint: fingerprint,
		Scope:       scope,
		TrustedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.entryPath(ownerKey), data, 0o600)
}

// Fingerprint returns the stored fingerprint for ownerKey, or "" if not found.
func (s *AnchorTrustStore) Fingerprint(ownerKey string) (string, error) {
	entry, err := s.load(ownerKey)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return entry.Fingerprint, nil
}

func (s *AnchorTrustStore) load(ownerKey string) (*AnchorTrustEntry, error) {
	data, err := os.ReadFile(s.entryPath(ownerKey))
	if err != nil {
		return nil, err
	}
	var e AnchorTrustEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *AnchorTrustStore) entryPath(ownerKey string) string {
	safe := strings.NewReplacer("/", "_", ":", "_").Replace(ownerKey)
	return filepath.Join(s.dir, safe+".json")
}
