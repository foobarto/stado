package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrRuntimeIdentityNotFound = errors.New("installed plugin has no matching lock identity")
var bundledRuntimeSourceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// RuntimeIdentity is the host-authenticated identity of one executable plugin
// instance. It is deliberately separate from Manifest.Name, which remains a
// display/legacy install alias and is not globally unique (EP-0039, EP-0063).
type RuntimeIdentity struct {
	Canonical      string `json:"canonical"`
	Namespace      string `json:"namespace"`
	ResolvedCommit string `json:"resolved_commit,omitempty"`
	ManifestDigest string `json:"manifest_digest"`
}

func (id RuntimeIdentity) Validate() error {
	if strings.TrimSpace(id.Canonical) == "" {
		return errors.New("canonical plugin identity required")
	}
	if strings.TrimSpace(id.Namespace) == "" || strings.TrimSpace(id.Namespace) != id.Namespace || strings.ContainsAny(id.Namespace, "#@") {
		return errors.New("stable plugin namespace required")
	}
	if !strings.HasPrefix(id.ManifestDigest, "sha256:") || len(id.ManifestDigest) != len("sha256:")+64 {
		return errors.New("canonical manifest digest required")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(id.ManifestDigest, "sha256:")); err != nil {
		return errors.New("canonical manifest digest required")
	}
	if !strings.HasPrefix(id.Canonical, id.Namespace+"@") {
		return errors.New("canonical plugin identity must use its stable namespace")
	}
	if id.ResolvedCommit != "" && (!shaRE.MatchString(id.ResolvedCommit) || len(id.ResolvedCommit) != 40) {
		return errors.New("resolved plugin commit must be a full 40-character SHA")
	}
	return nil
}

// ValidateManifest proves that the canonical identity was minted for the exact
// signed manifest being executed, not merely that its fields are well formed.
func (id RuntimeIdentity) ValidateManifest(manifest Manifest) error {
	if err := id.Validate(); err != nil {
		return err
	}
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return err
	}
	if id.ManifestDigest != digest {
		return errors.New("runtime identity does not match plugin manifest")
	}
	return nil
}

func (id RuntimeIdentity) QualifiedKind(localName string) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	if !artifactKindNameRE.MatchString(localName) {
		return "", fmt.Errorf("invalid local artifact kind %q", localName)
	}
	return id.Namespace + "#" + localName, nil
}

func RuntimeIdentityForInstalled(id Identity, manifest Manifest, resolvedCommit string) (RuntimeIdentity, error) {
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	out := RuntimeIdentity{Canonical: id.Canonical(), Namespace: id.Namespace(), ResolvedCommit: resolvedCommit, ManifestDigest: digest}
	return out, out.Validate()
}

// RuntimeIdentityFromLock binds an installed manifest to its EP-39 source
// identity. Matching by the signed wasm digest and version avoids trusting the
// display/install alias. More than one matching source is ambiguous and fails
// closed rather than choosing one by directory order.
func RuntimeIdentityFromLock(lock *Lock, manifest Manifest) (RuntimeIdentity, error) {
	if lock == nil {
		return RuntimeIdentity{}, errors.New("plugin lock required")
	}
	var matched *Identity
	for _, entry := range lock.Entries {
		if entry.WASMSHA256 == "" || entry.WASMSHA256 != manifest.WASMSHA256 {
			continue
		}
		id, err := ParseIdentity(entry.Identity)
		if err != nil {
			return RuntimeIdentity{}, fmt.Errorf("plugin lock identity %q: %w", entry.Identity, err)
		}
		if strings.TrimPrefix(id.Version, "v") != strings.TrimPrefix(manifest.Version, "v") {
			continue
		}
		if matched != nil && matched.Canonical() != id.Canonical() {
			return RuntimeIdentity{}, errors.New("installed plugin source identity is ambiguous")
		}
		copyID := id
		matched = &copyID
	}
	if matched == nil {
		return RuntimeIdentity{}, ErrRuntimeIdentityNotFound
	}
	resolvedCommit := ""
	if shaRE.MatchString(matched.Version) {
		resolvedCommit = matched.Version
	}
	return RuntimeIdentityForInstalled(*matched, manifest, resolvedCommit)
}

// RuntimeIdentityForBundledSource binds a bundled manifest to the trusted
// release-catalog name supplied by the native loader. The manifest display
// name is deliberately not consulted: a guest-controlled alias is not a
// source identity.
func RuntimeIdentityForBundledSource(source string, manifest Manifest) (RuntimeIdentity, error) {
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	name := strings.TrimSpace(source)
	if !bundledRuntimeSourceRE.MatchString(name) {
		return RuntimeIdentity{}, errors.New("valid bundled plugin source required")
	}
	out := RuntimeIdentity{
		Canonical:      "stado.dev/bundled/" + name + "@" + manifest.Version,
		Namespace:      "stado.dev/bundled/" + name,
		ManifestDigest: digest,
	}
	return out, out.Validate()
}

// RuntimeIdentityForBundled is retained for non-execution compatibility
// callers while they move to an explicit release-catalog source. Production
// plugin loaders must use RuntimeIdentityForBundledSource.
func RuntimeIdentityForBundled(manifest Manifest) (RuntimeIdentity, error) {
	return RuntimeIdentityForBundledSource(strings.TrimPrefix(manifest.Name, "stado-builtin-tool-"), manifest)
}

// RuntimeIdentityForLocalSource binds a development plugin to its canonical
// source path. Local identities are intentionally unstable distribution
// identities, but two source trees using the same manifest display name must
// never share an authority namespace.
func RuntimeIdentityForLocalSource(manifest Manifest, source string) (RuntimeIdentity, error) {
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return RuntimeIdentity{}, errors.New("local plugin source required")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return RuntimeIdentity{}, fmt.Errorf("local plugin source: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = filepath.Clean(resolved)
	}
	sum := sha256.Sum256([]byte(abs))
	namespace := "local://sha256/" + hex.EncodeToString(sum[:])
	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		version = "dev"
	}
	out := RuntimeIdentity{
		Canonical:      namespace + "@" + version,
		Namespace:      namespace,
		ManifestDigest: digest,
	}
	return out, out.Validate()
}

// RuntimeIdentityForLocal is a compatibility helper for tests and data
// migration code that has no executable source path. Production execution
// must call RuntimeIdentityForLocalSource with the verified plugin directory.
func RuntimeIdentityForLocal(manifest Manifest) (RuntimeIdentity, error) {
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	// The manifest digest keeps two compatibility identities in the same test
	// process separate without treating Manifest.Name as authority.
	return RuntimeIdentityForLocalSource(manifest, filepath.Join(cwd, ".stado-local-"+strings.TrimPrefix(digest, "sha256:")))
}
