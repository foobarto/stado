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
	SourceRevision string `json:"source_revision,omitempty"`
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
	if id.SourceRevision != "" && (strings.TrimSpace(id.SourceRevision) != id.SourceRevision || strings.ContainsAny(id.SourceRevision, "#@\x00")) {
		return errors.New("valid plugin source revision required")
	}
	if (id.SourceRevision == "") != (id.ResolvedCommit == "") {
		return errors.New("plugin source revision and resolved commit must be recorded together")
	}
	if id.SourceRevision != "" && (!shaRE.MatchString(id.ResolvedCommit) || len(id.ResolvedCommit) != 40) {
		return errors.New("remote plugin identity requires a dereferenced full commit")
	}
	if id.ResolvedCommit != "" && (!shaRE.MatchString(id.ResolvedCommit) || len(id.ResolvedCommit) != 40) {
		return errors.New("resolved plugin commit must be a full 40-character SHA")
	}
	if shaRE.MatchString(id.SourceRevision) && id.ResolvedCommit != id.SourceRevision {
		return errors.New("commit source revision and resolved commit must match")
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
	return RuntimeIdentityForResolvedInstall(id, manifest, id.Version, resolvedCommit)
}

// RuntimeIdentityForResolvedInstall binds a signed package to the logical
// source identity and the exact ref actually fetched. Package semver remains
// in the signed manifest and lock; a commit selector is never compared to it.
func RuntimeIdentityForResolvedInstall(id Identity, manifest Manifest, sourceRevision, resolvedCommit string) (RuntimeIdentity, error) {
	if _, err := id.PackageVersion(manifest); err != nil {
		return RuntimeIdentity{}, err
	}
	if err := id.ValidateSourceRevision(sourceRevision); err != nil {
		return RuntimeIdentity{}, err
	}
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	if !shaRE.MatchString(resolvedCommit) {
		return RuntimeIdentity{}, errors.New("resolved plugin source commit must be a full 40-character SHA")
	}
	if id.IsCommit() && resolvedCommit != sourceRevision {
		return RuntimeIdentity{}, errors.New("commit source revision and resolved commit must match")
	}
	out := RuntimeIdentity{Canonical: id.Canonical(), Namespace: id.Namespace(), SourceRevision: sourceRevision, ResolvedCommit: resolvedCommit, ManifestDigest: digest}
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
	var matchedSourceRevision string
	var matchedResolvedCommit string
	for _, entry := range lock.Entries {
		if entry.WASMSHA256 == "" || entry.WASMSHA256 != manifest.WASMSHA256 {
			continue
		}
		id, err := ParseIdentity(entry.Identity)
		if err != nil {
			return RuntimeIdentity{}, fmt.Errorf("plugin lock identity %q: %w", entry.Identity, err)
		}
		packageVersion := entry.PackageVersion
		if packageVersion == "" {
			if id.IsCommit() {
				return RuntimeIdentity{}, fmt.Errorf("legacy commit lock %q has no signed package_version", entry.Identity)
			}
			packageVersion = strings.TrimPrefix(id.Version, "v")
		}
		if strings.TrimPrefix(packageVersion, "v") != strings.TrimPrefix(manifest.Version, "v") {
			return RuntimeIdentity{}, fmt.Errorf("lock package version %q does not match signed manifest %q", packageVersion, manifest.Version)
		}
		if entry.AnchorFpr != "" && entry.AnchorFpr != manifest.AuthorPubkeyFpr {
			return RuntimeIdentity{}, errors.New("lock anchor fingerprint does not match signed manifest")
		}
		manifestDigest, err := manifest.ManifestDigest()
		if err != nil {
			return RuntimeIdentity{}, err
		}
		if entry.ManifestDigest == "" || entry.ManifestDigest != manifestDigest {
			return RuntimeIdentity{}, errors.New("lock canonical manifest digest does not match installed manifest")
		}
		sourceRevision := entry.SourceRevision
		if sourceRevision == "" {
			sourceRevision = id.Version
		}
		if err := id.ValidateSourceRevision(sourceRevision); err != nil {
			return RuntimeIdentity{}, fmt.Errorf("lock %q: %w", entry.Identity, err)
		}
		if !shaRE.MatchString(entry.ResolvedCommit) {
			return RuntimeIdentity{}, fmt.Errorf("lock %q has no valid dereferenced full commit", entry.Identity)
		}
		if id.IsCommit() && entry.ResolvedCommit != sourceRevision {
			return RuntimeIdentity{}, fmt.Errorf("lock %q commit source and resolved commit differ", entry.Identity)
		}
		if matched != nil {
			return RuntimeIdentity{}, errors.New("installed plugin source identity is duplicated or ambiguous")
		}
		copyID := id
		matched = &copyID
		matchedSourceRevision = sourceRevision
		matchedResolvedCommit = entry.ResolvedCommit
	}
	if matched == nil {
		return RuntimeIdentity{}, ErrRuntimeIdentityNotFound
	}
	return RuntimeIdentityForResolvedInstall(*matched, manifest, matchedSourceRevision, matchedResolvedCommit)
}

// RuntimeIdentityForInstalledDir resolves an installed package's exact remote
// lock identity. A package with no matching lock evidence is treated as an
// explicitly local installation and bound to its concrete install directory.
// Malformed, mismatched, duplicate, or incomplete matching lock evidence fails
// closed and is never reclassified as local.
func RuntimeIdentityForInstalledDir(pluginDir string, manifest Manifest) (RuntimeIdentity, error) {
	record, err := ReadInstallRecord(pluginDir, manifest)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	return RuntimeIdentityForInstallRecord(filepath.Dir(pluginDir), record, manifest)
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
