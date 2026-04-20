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
	Pubkey      string    `json:"pubkey"`   // hex
	Author      string    `json:"author,omitempty"`
	Pinned      time.Time `json:"pinned_at"`
	// Rollback protection: the highest manifest version (string compare —
	// callers should use semver-compatible strings like "1.2.3").
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
	data, err := os.ReadFile(s.Path)
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
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	list := make([]TrustEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Fingerprint < list[j].Fingerprint })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Trust adds a signer. Key may be passed as hex (64 chars) or base64 (44
// chars with padding). Author is optional but recommended for UIs.
func (s *TrustStore) Trust(key string, author string) (TrustEntry, error) {
	pub, err := parsePubkey(key)
	if err != nil {
		return TrustEntry{}, err
	}
	store, err := s.Load()
	if err != nil {
		return TrustEntry{}, err
	}
	entry := TrustEntry{
		Fingerprint: Fingerprint(pub),
		Pubkey:      hex.EncodeToString(pub),
		Author:      author,
		Pinned:      time.Now().UTC(),
	}
	store[entry.Fingerprint] = entry
	if err := s.Save(store); err != nil {
		return TrustEntry{}, err
	}
	return entry, nil
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
//   1. Is the declared author pubkey pinned?
//   2. Does the signature verify against that pubkey?
//   3. Is manifest.Version >= LastVersion (rollback protection)?
//
// On success, advances LastVersion to the manifest's version.
func (s *TrustStore) VerifyManifest(m *Manifest, sigB64 string) error {
	store, err := s.Load()
	if err != nil {
		return err
	}
	entry, ok := store[m.AuthorPubkeyFpr]
	if !ok {
		// The manifest only carries a fingerprint, not the full pubkey —
		// the Ed25519 pubkey isn't recoverable from the signature alone.
		// Point the user at both canonical paths: out-of-band-known key
		// goes through `plugin trust`; first-trust-on-install goes
		// through `--signer <pubkey>` (TOFU) so they don't have to
		// pin out-of-band before they even look at the plugin.
		return fmt.Errorf("verify: author fingerprint %s not pinned — obtain the author's pubkey out-of-band and run `stado plugin trust <pubkey>`, or retry with `stado plugin verify . --signer <pubkey>` to pin on first use (TOFU)", m.AuthorPubkeyFpr)
	}
	pub, err := hex.DecodeString(entry.Pubkey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("verify: trust-store pubkey malformed")
	}
	if err := m.Verify(ed25519.PublicKey(pub), sigB64); err != nil {
		return err
	}
	if entry.LastVersion != "" && m.Version < entry.LastVersion {
		return fmt.Errorf("verify: rollback detected — manifest %s < last seen %s", m.Version, entry.LastVersion)
	}
	// Advance LastVersion on successful verification.
	entry.LastVersion = m.Version
	store[entry.Fingerprint] = entry
	return s.Save(store)
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
