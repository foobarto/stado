package plugins

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TrustEntry is one pinned plugin signer.
type TrustEntry struct {
	Fingerprint string    `json:"fingerprint"`
	Pubkey      string    `json:"pubkey"` // hex
	Author      string    `json:"author,omitempty"`
	Pinned      time.Time `json:"pinned_at"`
	// VersionFloors is rollback protection keyed by the host-authenticated
	// package/source namespace. One offline signer may publish many
	// independently-versioned packages; a signer-global version floor makes
	// those packages reject one another.
	VersionFloors map[string]string `json:"version_floors,omitempty"`
	// AnchorOwners records owner identities whose published anchor key was
	// verified and accepted in the same atomic trust-store write as this key.
	// This replaces the split owner-fingerprint + signer-key transaction for
	// new remote installs.
	AnchorOwners []string `json:"anchor_owners,omitempty"`
	// LastVersion is the pre-v0.80 signer-global floor retained only as inert
	// audit evidence. It is never migrated into or consulted as source authority.
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("trust store has trailing JSON value")
		}
		return nil, err
	}
	out := make(map[string]TrustEntry, len(entries))
	for _, e := range entries {
		if _, duplicate := out[e.Fingerprint]; duplicate {
			return nil, fmt.Errorf("trust store has duplicate signer %s", e.Fingerprint)
		}
		pub, err := hex.DecodeString(e.Pubkey)
		if err != nil || len(pub) != ed25519.PublicKeySize || Fingerprint(ed25519.PublicKey(pub)) != e.Fingerprint {
			return nil, fmt.Errorf("trust store pubkey fingerprint mismatch or malformed key for signer %s", e.Fingerprint)
		}
		if e.LastVersion != "" {
			if err := ValidateVersion(e.LastVersion); err != nil {
				return nil, fmt.Errorf("trust store signer %s has invalid legacy rollback floor %q: %w", e.Fingerprint, e.LastVersion, err)
			}
		}
		e.VersionFloors = cloneVersionFloors(e.VersionFloors)
		for namespace, floor := range e.VersionFloors {
			if err := validatePackageNamespace(namespace); err != nil {
				return nil, fmt.Errorf("trust store signer %s: %w", e.Fingerprint, err)
			}
			if err := ValidateVersion(floor); err != nil {
				return nil, fmt.Errorf("trust store signer %s package %s has invalid rollback floor %q: %w", e.Fingerprint, namespace, floor, err)
			}
		}
		owners, ownerErr := normalizedOwners(e.AnchorOwners)
		if ownerErr != nil {
			return nil, fmt.Errorf("trust store signer %s: %w", e.Fingerprint, ownerErr)
		}
		e.AnchorOwners = owners
		out[e.Fingerprint] = e
	}
	return out, nil
}

// Save replaces the complete trust store under the same cross-process lock as
// incremental mutations. It is primarily an administrative/test primitive;
// production read-modify-write callers must mutate inside their own locked
// transaction and call saveUnlocked.
func (s *TrustStore) Save(entries map[string]TrustEntry) error {
	return withPluginFileLock(s.Path, func() error { return s.saveUnlocked(entries) })
}

func (s *TrustStore) saveUnlocked(entries map[string]TrustEntry) error {
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
	var result TrustEntry
	err := withPluginFileLock(s.Path, func() error {
		entry, _, store, err := s.entryForKey(key, author)
		if err != nil {
			return err
		}
		store[entry.Fingerprint] = entry
		if err := s.saveUnlocked(store); err != nil {
			return err
		}
		result = entry
		return nil
	})
	return result, err
}

// TrustVerifiedPackage pins key only after it verifies the exact manifest and
// advances rollback state for one host-authenticated package namespace.
func (s *TrustStore) TrustVerifiedPackage(key, author, packageNamespace string, m *Manifest, sigB64 string) (TrustEntry, error) {
	return s.trustVerified(key, author, packageNamespace, "", m, sigB64)
}

// TrustVerifiedAnchor is the remote-install transaction: signature,
// package-scoped rollback floor, signer key, and accepted owner anchor are
// committed in one atomic trusted_keys.json replacement. A failed signature or
// package check leaves neither owner nor signer trust behind.
func (s *TrustStore) TrustVerifiedAnchor(key, author, packageNamespace, owner string, m *Manifest, sigB64 string) (TrustEntry, error) {
	return s.trustVerified(key, author, packageNamespace, owner, m, sigB64)
}

func (s *TrustStore) trustVerified(key, author, packageNamespace, owner string, m *Manifest, sigB64 string) (TrustEntry, error) {
	var result TrustEntry
	err := withPluginFileLock(s.Path, func() error {
		entry, err := s.trustVerifiedLocked(key, author, packageNamespace, owner, m, sigB64)
		result = entry
		return err
	})
	return result, err
}

func (s *TrustStore) trustVerifiedLocked(key, author, packageNamespace, owner string, m *Manifest, sigB64 string) (TrustEntry, error) {
	if m == nil {
		return TrustEntry{}, fmt.Errorf("verify: nil manifest")
	}
	if err := validatePackageNamespace(packageNamespace); err != nil {
		return TrustEntry{}, err
	}
	if owner != "" {
		if err := validateOwnerKey(owner); err != nil {
			return TrustEntry{}, err
		}
	}
	// Hard-deny revoked fingerprints (seed leaked in git history; see
	// SECURITY.md). Refuse before pinning so a TOFU install can't anchor
	// a known-compromised key.
	if rev, _ := IsRevoked(m.AuthorPubkeyFpr); rev {
		return TrustEntry{}, RevokedError(m.AuthorPubkeyFpr)
	}
	entry, pub, store, err := s.entryForKey(key, author)
	if err != nil {
		return TrustEntry{}, err
	}
	if entry.Fingerprint != m.AuthorPubkeyFpr {
		return TrustEntry{}, fmt.Errorf("verify: signer fingerprint %s does not match manifest author_pubkey_fpr %s",
			entry.Fingerprint, m.AuthorPubkeyFpr)
	}
	floor := entry.VersionFloors[packageNamespace]
	if err := verifyManifestWithPub(m, sigB64, pub, floor); err != nil {
		return TrustEntry{}, err
	}
	if entry.VersionFloors == nil {
		entry.VersionFloors = make(map[string]string)
	}
	entry.VersionFloors[packageNamespace] = m.Version
	if owner != "" {
		for fingerprint, other := range store {
			if fingerprint != entry.Fingerprint && containsString(other.AnchorOwners, owner) {
				return TrustEntry{}, fmt.Errorf("verify: owner anchor %s is already pinned to signer %s", owner, fingerprint)
			}
		}
		entry.AnchorOwners = appendUniqueSorted(entry.AnchorOwners, owner)
	}
	store[entry.Fingerprint] = entry
	if err := s.saveUnlocked(store); err != nil {
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
		entry.VersionFloors = cloneVersionFloors(prev.VersionFloors)
		entry.AnchorOwners = append([]string(nil), prev.AnchorOwners...)
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
	return withPluginFileLock(s.Path, func() error {
		store, err := s.Load()
		if err != nil {
			return err
		}
		if _, ok := store[fingerprint]; !ok {
			return fmt.Errorf("untrust: fingerprint %s not pinned", fingerprint)
		}
		delete(store, fingerprint)
		return s.saveUnlocked(store)
	})
}

// VerifyManifestPackage verifies and advances the rollback floor for one exact
// package/source namespace.
func (s *TrustStore) VerifyManifestPackage(packageNamespace string, m *Manifest, sigB64 string) error {
	return withPluginFileLock(s.Path, func() error {
		entry, store, err := s.checkManifest(packageNamespace, m, sigB64)
		if err != nil {
			return err
		}
		if entry.VersionFloors == nil {
			entry.VersionFloors = make(map[string]string)
		}
		entry.VersionFloors[packageNamespace] = m.Version
		store[entry.Fingerprint] = entry
		return s.saveUnlocked(store)
	})
}

// CheckManifestPackage is the read-only runtime verification form.
func (s *TrustStore) CheckManifestPackage(packageNamespace string, m *Manifest, sigB64 string) error {
	_, _, err := s.checkManifest(packageNamespace, m, sigB64)
	return err
}

// CheckInstalledPackage requires both the package-local provenance and a
// user-state admission receipt written by the host install transaction. A
// project-controlled record/lock pair alone can never mint source authority.
func (s *TrustStore) CheckInstalledPackage(pluginDir string, m *Manifest, sigB64 string) (RuntimeIdentity, error) {
	if m == nil {
		return RuntimeIdentity{}, fmt.Errorf("verify: nil manifest")
	}
	if err := CheckManifestHostVersion(m); err != nil {
		return RuntimeIdentity{}, fmt.Errorf("verify: host compatibility: %w", err)
	}
	record, err := ReadInstallRecord(pluginDir, *m)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	stateDir := filepath.Dir(filepath.Dir(s.Path))
	if err := CheckInstallReceipt(stateDir, filepath.Dir(pluginDir), record); err != nil {
		return RuntimeIdentity{}, err
	}
	identity, err := RuntimeIdentityForInstallRecord(filepath.Dir(pluginDir), record, *m)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	if record.Kind == InstallRemote {
		remoteID, parseErr := ParseIdentity(record.CanonicalSource)
		if parseErr != nil {
			return RuntimeIdentity{}, parseErr
		}
		anchor, ok, anchorErr := s.AnchorSigner(remoteID.OwnerKey())
		if anchorErr != nil {
			return RuntimeIdentity{}, anchorErr
		}
		if !ok || anchor.Fingerprint != record.SignerFingerprint {
			return RuntimeIdentity{}, errors.New("remote installed plugin has no exact accepted owner-anchor binding")
		}
	}
	if err := s.CheckManifestPackage(identity.Namespace, m, sigB64); err != nil {
		return RuntimeIdentity{}, err
	}
	return identity, nil
}

func (s *TrustStore) checkManifest(packageNamespace string, m *Manifest, sigB64 string) (TrustEntry, map[string]TrustEntry, error) {
	if m == nil {
		return TrustEntry{}, nil, fmt.Errorf("verify: nil manifest")
	}
	if err := validatePackageNamespace(packageNamespace); err != nil {
		return TrustEntry{}, nil, err
	}
	// Hard-deny revoked fingerprints — even if previously trusted, refuse
	// to verify a manifest signed by a key whose private seed leaked in
	// git history (see SECURITY.md).
	if rev, _ := IsRevoked(m.AuthorPubkeyFpr); rev {
		return TrustEntry{}, nil, RevokedError(m.AuthorPubkeyFpr)
	}
	store, err := s.Load()
	if err != nil {
		return TrustEntry{}, nil, err
	}
	entry, ok := store[m.AuthorPubkeyFpr]
	if !ok {
		// The manifest only carries a fingerprint, not the full pubkey —
		// the Ed25519 pubkey isn't recoverable from the signature alone.
		// The user must acquire the full key out of band and make a
		// separate, explicit trust decision before verification retries.
		// Verification itself never mutates the trust store.
		return TrustEntry{}, nil, fmt.Errorf("verify: author fingerprint %s not pinned — obtain the author's pubkey out-of-band, run `stado plugin trust <pubkey>`, then retry verification", m.AuthorPubkeyFpr)
	}
	if entry.Fingerprint != m.AuthorPubkeyFpr {
		return TrustEntry{}, nil, fmt.Errorf("verify: trust-store fingerprint mismatch: entry %s for manifest %s",
			entry.Fingerprint, m.AuthorPubkeyFpr)
	}
	pub, err := hex.DecodeString(entry.Pubkey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return TrustEntry{}, nil, fmt.Errorf("verify: trust-store pubkey malformed")
	}
	if got := Fingerprint(ed25519.PublicKey(pub)); got != entry.Fingerprint {
		return TrustEntry{}, nil, fmt.Errorf("verify: trust-store pubkey fingerprint mismatch: got %s, want %s",
			got, entry.Fingerprint)
	}
	floor := entry.VersionFloors[packageNamespace]
	if err := verifyManifestWithPub(m, sigB64, ed25519.PublicKey(pub), floor); err != nil {
		return TrustEntry{}, nil, err
	}
	return entry, store, nil
}

// AnchorSigner returns the exact signer entry atomically associated with an
// accepted owner anchor. Duplicate owner assignments are corruption and fail
// closed rather than selecting by file order.
func (s *TrustStore) AnchorSigner(owner string) (TrustEntry, bool, error) {
	if err := validateOwnerKey(owner); err != nil {
		return TrustEntry{}, false, err
	}
	store, err := s.Load()
	if err != nil {
		return TrustEntry{}, false, err
	}
	var matched TrustEntry
	found := false
	for _, entry := range store {
		if !containsString(entry.AnchorOwners, owner) {
			continue
		}
		if found {
			return TrustEntry{}, false, fmt.Errorf("verify: owner anchor %s is assigned to multiple signers", owner)
		}
		matched, found = entry, true
	}
	return matched, found, nil
}

// RemoveAnchor removes only the owner-to-signer association. The signer pin
// and package rollback history remain intact for local packages and forensic
// continuity; the next remote install performs first-sight anchor trust again.
func (s *TrustStore) RemoveAnchor(owner string) error {
	if err := validateOwnerKey(owner); err != nil {
		return err
	}
	return withPluginFileLock(s.Path, func() error {
		store, err := s.Load()
		if err != nil {
			return err
		}
		changed := false
		for fingerprint, entry := range store {
			kept := entry.AnchorOwners[:0]
			for _, candidate := range entry.AnchorOwners {
				if candidate == owner {
					changed = true
					continue
				}
				kept = append(kept, candidate)
			}
			entry.AnchorOwners = append([]string(nil), kept...)
			store[fingerprint] = entry
		}
		if !changed {
			return nil
		}
		return s.saveUnlocked(store)
	})
}

func validatePackageNamespace(namespace string) error {
	if strings.TrimSpace(namespace) != namespace || namespace == "" || len(namespace) > 512 || strings.ContainsAny(namespace, "@#\x00") {
		return fmt.Errorf("verify: valid canonical package namespace required")
	}
	return nil
}

func validateOwnerKey(owner string) error {
	if strings.TrimSpace(owner) != owner || owner == "" || len(owner) > 512 || strings.Count(owner, "/") != 1 || strings.ContainsAny(owner, "@#\x00") {
		return fmt.Errorf("verify: valid owner anchor key required")
	}
	return nil
}

func cloneVersionFloors(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func normalizedOwners(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, owner := range in {
		if err := validateOwnerKey(owner); err != nil {
			return nil, err
		}
		out = appendUniqueSorted(out, owner)
	}
	return out, nil
}

func appendUniqueSorted(values []string, value string) []string {
	if !containsString(values, value) {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
