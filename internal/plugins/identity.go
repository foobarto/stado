package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Identity is the canonical, versioned identity of a remote plugin.
// Format: <host>/<owner>/<repo>[/<subdir...>]@<version>
// Version must be a semver tag (vX.Y.Z) or a full 40-char commit SHA.
// EP-0039 §A.
type Identity struct {
	Host    string
	Owner   string
	Repo    string
	Subdir  string // empty for top-level plugin in the repo
	Version string
}

var (
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	semverRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?(\+[a-zA-Z0-9.]+)?$`)
)

// ParseIdentity parses a plugin identity string.
// Floating versions (latest, main, HEAD, branch names) are rejected.
func ParseIdentity(raw string) (Identity, error) {
	atIdx := strings.LastIndex(raw, "@")
	if atIdx < 0 {
		return Identity{}, fmt.Errorf("identity %q: missing @version suffix", raw)
	}
	path, version := raw[:atIdx], raw[atIdx+1:]
	if err := validatePluginVersion(version); err != nil {
		return Identity{}, fmt.Errorf("identity %q: %w", raw, err)
	}
	// path: host/owner/repo[/subdir...]
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 3 {
		return Identity{}, fmt.Errorf("identity %q: expected <host>/<owner>/<repo>", raw)
	}
	id := Identity{
		Host:    parts[0],
		Owner:   parts[1],
		Repo:    parts[2],
		Version: version,
	}
	if len(parts) == 4 {
		id.Subdir = parts[3]
	}
	return id, nil
}

// validatePluginVersion rejects floating version specs.
func validatePluginVersion(v string) error {
	if shaRE.MatchString(v) {
		return nil
	}
	if semverRE.MatchString(v) {
		return nil
	}
	return fmt.Errorf("version %q is not a semver tag (vX.Y.Z) or full 40-char SHA; "+
		"floating versions (latest, main, HEAD, branch names) are not allowed — "+
		"use `stado plugin update --check` to find the latest release tag", v)
}

// Key returns a stable 16-char hex string uniquely identifying this
// host+owner+repo+subdir+version combination. Used as install-dir key.
func (id Identity) Key() string {
	sum := sha256.Sum256([]byte(id.Canonical()))
	return hex.EncodeToString(sum[:])[:16]
}

// Canonical returns the normalised identity string.
func (id Identity) Canonical() string {
	s := id.Host + "/" + id.Owner + "/" + id.Repo
	if id.Subdir != "" {
		s += "/" + id.Subdir
	}
	return s + "@" + id.Version
}

// OwnerKey returns the owner-scoped identifier used for anchor trust.
// Format: <host>/<owner>
func (id Identity) OwnerKey() string {
	return id.Host + "/" + id.Owner
}

// AnchorURL returns the well-known URL for this owner's anchor pubkey.
// Format: https://<host>/<owner>/stado-plugins/raw/main/.stado/author.pub
func (id Identity) AnchorURL() string {
	return fmt.Sprintf("https://%s/%s/stado-plugins/raw/main/.stado/author.pub", id.Host, id.Owner)
}

// LocalAlias returns the default local alias derived from the last path segment.
func (id Identity) LocalAlias() string {
	if id.Subdir != "" {
		parts := strings.Split(id.Subdir, "/")
		return parts[len(parts)-1]
	}
	return id.Repo
}
