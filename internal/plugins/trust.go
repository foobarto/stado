package plugins

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// TrustEntry is one pinned plugin signer.
type TrustEntry struct {
	Fingerprint string    `json:"fingerprint"`
	Pubkey      string    `json:"pubkey"` // hex
	Author      string    `json:"author,omitempty"`
	Pinned      time.Time `json:"pinned_at"`
	// Rollback protection: the highest semver-compatible manifest version.
	LastVersion string `json:"last_version,omitempty"`
}

// TrustStore is a file-backed set of TrustEntry records.
type TrustStore struct {
	Path string
}

// NewTrustStore points at stado's default location under XDG_DATA_HOME.
func NewTrustStore(stateDir string) *TrustStore {
	return &TrustStore{Path: filepath.Join(stateDir, "plugins", "trusted_keys.json")}
}

// Load reads the store from disk. Missing file = empty store (first run).
func (s *TrustStore) Load() (map[string]TrustEntry, error) {
	data, err := readPluginStateFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]TrustEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []TrustEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	out := make(map[string]TrustEntry, len(entries))
	for _, e := range entries {
		out[e.Fingerprint] = e
	}
	return out, nil
}

// Save writes entries back to disk (0600, atomically via rename).
func (s *TrustStore) Save(entries map[string]TrustEntry) error {
	list := make([]TrustEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Fingerprint < list[j].Fingerprint })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writePluginStateFileAtomic(s.Path, data, 0o600)
}

// Trust adds a signer. Key may be passed as hex (64 chars) or base64 (44
// chars with padding). Author is optional but recommended for UIs.
func (s *TrustStore) Trust(key string, author string) (TrustEntry, error) {
	entry, _, store, err := s.entryForKey(key, author)
	if err != nil {
		return TrustEntry{}, err
	}
	store[entry.Fingerprint] = entry
	if err := s.Save(store); err != nil {
		return TrustEntry{}, err
	}
	return entry, nil
}

// TrustVerified pins key only after it matches m.AuthorPubkeyFpr and verifies
// the manifest signature + rollback checks. This keeps TOFU install paths from
// leaving behind trust-store entries after failed verification.
func (s *TrustStore) TrustVerified(key string, author string, m *Manifest, sigB64 string) (TrustEntry, error) {
	if m == nil {
		return TrustEntry{}, fmt.Errorf("verify: nil manifest")
	}
	entry, pub, store, err := s.entryForKey(key, author)
	if err != nil {
		return TrustEntry{}, err
	}
	if entry.Fingerprint != m.AuthorPubkeyFpr {
		return TrustEntry{}, fmt.Errorf("verify: signer fingerprint %s does not match manifest author_pubkey_fpr %s",
			entry.Fingerprint, m.AuthorPubkeyFpr)
	}
	if err := verifyManifestWithPub(m, sigB64, pub, entry.LastVersion); err != nil {
		return TrustEntry{}, err
	}
	entry.LastVersion = m.Version
	store[entry.Fingerprint] = entry
	if err := s.Save(store); err != nil {
		return TrustEntry{}, err
	}
	return entry, nil
}

func (s *TrustStore) entryForKey(key string, author string) (TrustEntry, ed25519.PublicKey, map[string]TrustEntry, error) {
	pub, err := parsePubkey(key)
	if err != nil {
		return TrustEntry{}, nil, nil, err
	}
	store, err := s.Load()
	if err != nil {
		return TrustEntry{}, nil, nil, err
	}
	fpr := Fingerprint(pub)
	now := time.Now().UTC()
	entry := TrustEntry{
		Fingerprint: fpr,
		Pubkey:      hex.EncodeToString(pub),
		Author:      author,
		Pinned:      now,
	}
	if prev, ok := store[fpr]; ok {
		entry.LastVersion = prev.LastVersion
		if author == "" {
			entry.Author = prev.Author
		}
		if !prev.Pinned.IsZero() {
			entry.Pinned = prev.Pinned
		}
	}
	return entry, pub, store, nil
}

// Untrust removes the signer with the given fingerprint.
func (s *TrustStore) Untrust(fingerprint string) error {
	store, err := s.Load()
	if err != nil {
		return err
	}
	if _, ok := store[fingerprint]; !ok {
		return fmt.Errorf("untrust: fingerprint %s not pinned", fingerprint)
	}
	delete(store, fingerprint)
	return s.Save(store)
}

// VerifyManifest checks a loaded manifest + its signature against the
// TrustStore. Checks:
//  1. Is the declared author pubkey pinned?
//  2. Does the signature verify against that pubkey?
//  3. Is manifest.Version >= LastVersion (rollback protection)?
//
// On success, advances LastVersion to the manifest's version.
func (s *TrustStore) VerifyManifest(m *Manifest, sigB64 string) error {
	if m == nil {
		return fmt.Errorf("verify: nil manifest")
	}
	store, err := s.Load()
	if err != nil {
		return err
	}
	entry, ok := store[m.AuthorPubkeyFpr]
	if !ok {
		// The manifest only carries a fingerprint, not the full pubkey —
		// the Ed25519 pubkey isn't recoverable from the signature alone.
		// Do NOT recommend `stado plugin trust <pubkey>` with the
		// author.pubkey sidecar — doing so silently downgrades security
		// to TOFU (trust on first use) without the user's explicit
		// consent. The only safe in-band path is the explicit --signer
		// flag on install/verify, which the user must provide themselves.
		return fmt.Errorf("verify: author fingerprint %s not pinned — obtain the author's pubkey out-of-band and run `stado plugin trust <pubkey>`, or retry with `stado plugin verify . --signer <pubkey>` to pin on first use (TOFU)", m.AuthorPubkeyFpr)
	}
	if entry.Fingerprint != m.AuthorPubkeyFpr {
		return fmt.Errorf("verify: trust-store fingerprint mismatch: entry %s for manifest %s",
			entry.Fingerprint, m.AuthorPubkeyFpr)
	}
	pub, err := hex.DecodeString(entry.Pubkey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("verify: trust-store pubkey malformed")
	}
	if got := Fingerprint(ed25519.PublicKey(pub)); got != entry.Fingerprint {
		return fmt.Errorf("verify: trust-store pubkey fingerprint mismatch: got %s, want %s",
			got, entry.Fingerprint)
	}
	if err := verifyManifestWithPub(m, sigB64, ed25519.PublicKey(pub), entry.LastVersion); err != nil {
		return err
	}
	// Advance LastVersion on successful verification.
	entry.LastVersion = m.Version
	store[entry.Fingerprint] = entry
	return s.Save(store)
}

func verifyManifestWithPub(m *Manifest, sigB64 string, pub ed25519.PublicKey, lastVersion string) error {
	if err := m.Verify(pub, sigB64); err != nil {
		return err
	}
	if err := ValidateVersion(m.Version); err != nil {
		return fmt.Errorf("verify: manifest version %q is not semver-compatible: %w", m.Version, err)
	}
	if lastVersion != "" {
		less, err := VersionLess(m.Version, lastVersion)
		if err != nil {
			return fmt.Errorf("verify: compare versions: %w", err)
		}
		if less {
			return fmt.Errorf("verify: rollback detected — manifest %s < last seen %s", m.Version, lastVersion)
		}
	}
	return nil
}

// ParsePubkey is the exported wrapper around parsePubkey. Accepts hex
// (64 chars) or standard-encoded base64 (44 chars with padding).
func ParsePubkey(s string) (ed25519.PublicKey, error) {
	return parsePubkey(s)
}

// parsePubkey accepts hex (64 chars) or standard-encoded base64 (44 chars
// with padding). Returns an ed25519.PublicKey of the canonical 32 bytes.
func parsePubkey(s string) (ed25519.PublicKey, error) {
	if len(s) == ed25519.PublicKeySize*2 {
		raw, err := hex.DecodeString(s)
		if err == nil && len(raw) == ed25519.PublicKeySize {
			return ed25519.PublicKey(raw), nil
		}
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err == nil && len(raw) == ed25519.PublicKeySize {
		return ed25519.PublicKey(raw), nil
	}
	return nil, fmt.Errorf("plugin: bad pubkey; want 64-char hex or base64 of 32 bytes")
}
