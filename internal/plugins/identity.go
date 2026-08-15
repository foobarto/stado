package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
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
	shaRE       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	semverRE    = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?(\+[a-zA-Z0-9.]+)?$`)
	hostRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}[a-z0-9]$|^[a-z0-9]$`)
	pathPartRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	ownerPartRE = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
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
	if !hostRE.MatchString(parts[0]) {
		return Identity{}, fmt.Errorf("identity %q: invalid canonical host", raw)
	}
	if !ownerPartRE.MatchString(parts[1]) || !pathPartRE.MatchString(parts[2]) || parts[1] == "." || parts[1] == ".." || parts[2] == "." || parts[2] == ".." {
		return Identity{}, fmt.Errorf("identity %q: invalid owner or repository", raw)
	}
	id := Identity{
		Host:    parts[0],
		Owner:   parts[1],
		Repo:    parts[2],
		Version: version,
	}
	if len(parts) == 4 {
		for _, segment := range strings.Split(parts[3], "/") {
			if !pathPartRE.MatchString(segment) || segment == "." || segment == ".." {
				return Identity{}, fmt.Errorf("identity %q: invalid package subdirectory", raw)
			}
		}
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
	return id.Namespace() + "@" + id.Version
}

// PackageVersion validates and returns the signed manifest package version
// associated with this source selector. A semver source tag names the same
// package version (with only the conventional leading v omitted); an exact
// commit SHA is an independent source revision and therefore accepts any valid
// signed semver package version.
func (id Identity) PackageVersion(manifest Manifest) (string, error) {
	if err := ValidateVersion(manifest.Version); err != nil {
		return "", fmt.Errorf("identity %s: invalid manifest package version %q: %w", id.Canonical(), manifest.Version, err)
	}
	if shaRE.MatchString(id.Version) {
		return manifest.Version, nil
	}
	want := strings.TrimPrefix(id.Version, "v")
	got := strings.TrimPrefix(manifest.Version, "v")
	if want != got {
		return "", fmt.Errorf("identity %s selects package version %s but signed manifest declares %s", id.Canonical(), want, manifest.Version)
	}
	return manifest.Version, nil
}

// SourceRevisions returns the bounded source refs tried for an identity. A
// monorepo package prefers its EP-39 subdir-prefixed tag and may fall back to a
// repository-wide tag. Exact commit identities have one source revision.
func (id Identity) SourceRevisions() []string {
	if shaRE.MatchString(id.Version) || id.Subdir == "" {
		return []string{id.Version}
	}
	return []string{path.Clean(id.Subdir) + "/" + id.Version, id.Version}
}

// ValidateSourceRevision proves that a recorded ref came from the bounded
// resolution set for this logical package identity.
func (id Identity) ValidateSourceRevision(revision string) error {
	for _, allowed := range id.SourceRevisions() {
		if revision == allowed {
			return nil
		}
	}
	return fmt.Errorf("source revision %q is not valid for %s", revision, id.Canonical())
}

// IsCommit reports whether the identity is pinned directly to an immutable
// full commit rather than a semver source tag.
func (id Identity) IsCommit() bool { return shaRE.MatchString(id.Version) }

// Namespace is the stable, unversioned source identity. Artifact kinds use
// this namespace so a plugin upgrade does not create a different logical kind;
// the exact version/commit remains in the kind schema descriptor (EP-0063).
func (id Identity) Namespace() string {
	s := id.Host + "/" + id.Owner + "/" + id.Repo
	if id.Subdir != "" {
		s += "/" + id.Subdir
	}
	return s
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
