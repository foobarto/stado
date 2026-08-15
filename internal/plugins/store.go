package plugins

// The installed-plugin store is source-keyed. Manifest.Name is presentation
// metadata supplied by the guest and must never select an executable package.
// EP-0039 and EP-0066.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	InstallRecordFile      = ".stado-install.json"
	installRecordSchema    = 1
	maxInstallRecordBytes  = 32 << 10
	remoteInstallKeyPrefix = "remote-"
	localInstallKeyPrefix  = "local-"
)

type InstallKind string

const (
	InstallRemote InstallKind = "remote"
	InstallLocal  InstallKind = "local"
)

// InstallRecord is host-owned provenance for one copied package. Its StoreKey
// is the SHA-256 of every other field. This is not a privilege boundary against
// the same UID: local state can be replaced by that UID. It is an integrity and
// identity boundary that prevents code from falling back to guest-controlled
// Manifest.Name or from accidentally reopening a package as another source.
type InstallRecord struct {
	Schema            int         `json:"schema"`
	StoreKey          string      `json:"store_key"`
	Kind              InstallKind `json:"kind"`
	CanonicalSource   string      `json:"canonical_source"`
	Namespace         string      `json:"namespace"`
	SourceRevision    string      `json:"source_revision,omitempty"`
	ResolvedCommit    string      `json:"resolved_commit,omitempty"`
	PackageVersion    string      `json:"package_version"`
	ManifestDigest    string      `json:"manifest_digest"`
	WASMSHA256        string      `json:"wasm_sha256"`
	SignerFingerprint string      `json:"signer_fingerprint"`
}

type installRecordDigest struct {
	Schema            int         `json:"schema"`
	Kind              InstallKind `json:"kind"`
	CanonicalSource   string      `json:"canonical_source"`
	Namespace         string      `json:"namespace"`
	SourceRevision    string      `json:"source_revision,omitempty"`
	ResolvedCommit    string      `json:"resolved_commit,omitempty"`
	PackageVersion    string      `json:"package_version"`
	ManifestDigest    string      `json:"manifest_digest"`
	WASMSHA256        string      `json:"wasm_sha256"`
	SignerFingerprint string      `json:"signer_fingerprint"`
}

func (r InstallRecord) expectedStoreKey() (string, error) {
	payload, err := json.Marshal(installRecordDigest{
		Schema: r.Schema, Kind: r.Kind, CanonicalSource: r.CanonicalSource,
		Namespace: r.Namespace, SourceRevision: r.SourceRevision,
		ResolvedCommit: r.ResolvedCommit, PackageVersion: r.PackageVersion,
		ManifestDigest: r.ManifestDigest, WASMSHA256: r.WASMSHA256,
		SignerFingerprint: r.SignerFingerprint,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	prefix := remoteInstallKeyPrefix
	if r.Kind == InstallLocal {
		prefix = localInstallKeyPrefix
	}
	return prefix + hex.EncodeToString(sum[:]), nil
}

func (r InstallRecord) Validate(manifest Manifest) error {
	if r.Schema != installRecordSchema {
		return fmt.Errorf("unsupported installed-plugin record schema %d; reinstall the plugin with this stado version", r.Schema)
	}
	if r.Kind != InstallRemote && r.Kind != InstallLocal {
		return errors.New("invalid installed-plugin source kind")
	}
	if strings.TrimSpace(r.CanonicalSource) != r.CanonicalSource || r.CanonicalSource == "" ||
		strings.TrimSpace(r.Namespace) != r.Namespace || r.Namespace == "" {
		return errors.New("installed-plugin record requires canonical source and namespace")
	}
	if r.PackageVersion != manifest.Version || r.WASMSHA256 != manifest.WASMSHA256 ||
		r.SignerFingerprint != manifest.AuthorPubkeyFpr {
		return errors.New("installed-plugin record does not match signed manifest package fields")
	}
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return err
	}
	if r.ManifestDigest != digest {
		return errors.New("installed-plugin record does not match canonical signed manifest digest")
	}
	want, err := r.expectedStoreKey()
	if err != nil {
		return err
	}
	if r.StoreKey != want {
		return errors.New("installed-plugin store key does not bind the complete provenance record")
	}
	if r.Kind == InstallRemote {
		id, err := ParseIdentity(r.CanonicalSource)
		if err != nil {
			return fmt.Errorf("installed-plugin remote source: %w", err)
		}
		if id.Namespace() != r.Namespace {
			return errors.New("installed-plugin namespace does not match remote source")
		}
		if err := id.ValidateSourceRevision(r.SourceRevision); err != nil {
			return err
		}
		if !shaRE.MatchString(r.ResolvedCommit) {
			return errors.New("installed-plugin remote record requires a dereferenced full commit")
		}
		if id.IsCommit() && r.ResolvedCommit != r.SourceRevision {
			return errors.New("installed-plugin commit source and resolved commit differ")
		}
	} else {
		if r.SourceRevision != "" || r.ResolvedCommit != "" {
			return errors.New("local installed-plugin record cannot contain remote revisions")
		}
		identity, err := RuntimeIdentityForLocalSource(manifest, r.CanonicalSource)
		if err != nil {
			return err
		}
		if identity.Namespace != r.Namespace {
			return errors.New("installed-plugin namespace does not match canonical local source")
		}
	}
	return nil
}

func NewRemoteInstallRecord(id Identity, sourceRevision, resolvedCommit string, manifest Manifest) (InstallRecord, error) {
	identity, err := RuntimeIdentityForResolvedInstall(id, manifest, sourceRevision, resolvedCommit)
	if err != nil {
		return InstallRecord{}, err
	}
	r := InstallRecord{
		Schema: installRecordSchema, Kind: InstallRemote,
		CanonicalSource: id.Canonical(), Namespace: identity.Namespace,
		SourceRevision: sourceRevision, ResolvedCommit: resolvedCommit,
		PackageVersion: manifest.Version, ManifestDigest: identity.ManifestDigest,
		WASMSHA256: manifest.WASMSHA256, SignerFingerprint: manifest.AuthorPubkeyFpr,
	}
	r.StoreKey, err = r.expectedStoreKey()
	return r, err
}

func NewLocalInstallRecord(source string, manifest Manifest) (InstallRecord, error) {
	abs, err := canonicalLocalSource(source)
	if err != nil {
		return InstallRecord{}, err
	}
	identity, err := RuntimeIdentityForLocalSource(manifest, abs)
	if err != nil {
		return InstallRecord{}, err
	}
	r := InstallRecord{
		Schema: installRecordSchema, Kind: InstallLocal,
		CanonicalSource: abs, Namespace: identity.Namespace,
		PackageVersion: manifest.Version, ManifestDigest: identity.ManifestDigest,
		WASMSHA256: manifest.WASMSHA256, SignerFingerprint: manifest.AuthorPubkeyFpr,
	}
	r.StoreKey, err = r.expectedStoreKey()
	return r, err
}

func canonicalLocalSource(source string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil {
		return "", fmt.Errorf("local plugin source: %w", err)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve local plugin source: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func WriteInstallRecord(pluginDir string, record InstallRecord, manifest Manifest) error {
	if filepath.Base(pluginDir) != record.StoreKey {
		return errors.New("installed-plugin directory name does not match store key")
	}
	if err := record.Validate(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(pluginDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return workdirpath.NewRootResolver(root).WriteFileAtomic(InstallRecordFile, data, 0o600)
}

func ReadInstallRecord(pluginDir string, manifest Manifest) (InstallRecord, error) {
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(pluginDir)
	if err != nil {
		return InstallRecord{}, err
	}
	defer func() { _ = root.Close() }()
	data, err := workdirpath.NewRootResolver(root).ReadFileLimited(InstallRecordFile, maxInstallRecordBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return InstallRecord{}, fmt.Errorf("legacy flat installed-plugin directory %q has no %s; reinstall it explicitly (automatic identity inference is forbidden)", filepath.Base(pluginDir), InstallRecordFile)
		}
		return InstallRecord{}, err
	}
	var record InstallRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return InstallRecord{}, fmt.Errorf("parse installed-plugin record: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return InstallRecord{}, errors.New("parse installed-plugin record: trailing JSON value")
	}
	if filepath.Base(pluginDir) != record.StoreKey {
		return InstallRecord{}, errors.New("installed-plugin directory name does not match recorded store key")
	}
	if err := record.Validate(manifest); err != nil {
		return InstallRecord{}, err
	}
	return record, nil
}

// InstalledPackage is one validated source-keyed store entry.
type InstalledPackage struct {
	Dir       string
	Record    InstallRecord
	Manifest  Manifest
	Signature string
	Identity  RuntimeIdentity
}

// ListInstalledPackages enumerates only real, non-symlink directories and
// validates each complete install record. A legacy flat entry fails the whole
// discovery operation rather than being silently reinterpreted.
func ListInstalledPackages(root string) ([]InstalledPackage, error) {
	ids, err := ListInstalledDirs(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]InstalledPackage, 0, len(ids))
	for _, key := range ids {
		dir := filepath.Join(root, key)
		manifest, sig, err := LoadFromDir(dir)
		if err != nil {
			return nil, fmt.Errorf("installed plugin %s: %w", key, err)
		}
		record, err := ReadInstallRecord(dir, *manifest)
		if err != nil {
			return nil, fmt.Errorf("installed plugin %s: %w", key, err)
		}
		identity, err := RuntimeIdentityForInstallRecord(root, record, *manifest)
		if err != nil {
			return nil, fmt.Errorf("installed plugin %s: %w", key, err)
		}
		out = append(out, InstalledPackage{Dir: dir, Record: record, Manifest: *manifest, Signature: sig, Identity: identity})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Record.StoreKey < out[j].Record.StoreKey })
	return out, nil
}

func RuntimeIdentityForInstallRecord(pluginsRoot string, record InstallRecord, manifest Manifest) (RuntimeIdentity, error) {
	if err := record.Validate(manifest); err != nil {
		return RuntimeIdentity{}, err
	}
	if record.Kind == InstallLocal {
		return RuntimeIdentityForLocalSource(manifest, record.CanonicalSource)
	}
	lockPath := filepath.Join(filepath.Dir(pluginsRoot), "plugin-lock.toml")
	lock, err := ReadLock(lockPath)
	if err != nil {
		return RuntimeIdentity{}, fmt.Errorf("read exact remote source lock: %w", err)
	}
	var matched *LockEntry
	for i := range lock.Entries {
		entry := &lock.Entries[i]
		if entry.StoreKey != record.StoreKey {
			continue
		}
		if matched != nil {
			return RuntimeIdentity{}, errors.New("remote installed-plugin store key is duplicated in source lock")
		}
		matched = entry
	}
	if matched == nil {
		return RuntimeIdentity{}, errors.New("remote installed-plugin has no exact source-keyed lock row")
	}
	if matched.Identity != record.CanonicalSource || matched.SourceRevision != record.SourceRevision ||
		matched.ResolvedCommit != record.ResolvedCommit || matched.PackageVersion != record.PackageVersion ||
		matched.ManifestDigest != record.ManifestDigest || matched.WASMSHA256 != record.WASMSHA256 ||
		matched.AnchorFpr != record.SignerFingerprint {
		return RuntimeIdentity{}, errors.New("remote installed-plugin record does not exactly match its source lock row")
	}
	id, err := ParseIdentity(matched.Identity)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	return RuntimeIdentityForResolvedInstall(id, manifest, matched.SourceRevision, matched.ResolvedCommit)
}

// ResolveInstalledPackage resolves an exact store key/canonical source first.
// Manifest names are friendly aliases only and must be unique across every
// supplied root. Ambiguity always fails closed, including across scopes.
func ResolveInstalledPackage(roots []string, selector string) (InstalledPackage, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return InstalledPackage{}, errors.New("installed plugin selector required")
	}
	var exact, aliases []InstalledPackage
	for _, root := range roots {
		packages, err := ListInstalledPackages(root)
		if err != nil {
			return InstalledPackage{}, err
		}
		for _, pkg := range packages {
			if selector == pkg.Record.StoreKey || selector == pkg.Identity.Canonical {
				exact = append(exact, pkg)
			} else if selector == pkg.Manifest.Name || selector == pkg.Manifest.Name+"@"+pkg.Manifest.Version {
				aliases = append(aliases, pkg)
			}
		}
	}
	if len(exact) > 0 {
		if len(exact) != 1 {
			return InstalledPackage{}, fmt.Errorf("installed plugin selector %q is ambiguous across installed entries; use the exact store key", selector)
		}
		return exact[0], nil
	}
	if len(aliases) == 1 {
		return aliases[0], nil
	}
	if len(aliases) > 1 {
		return InstalledPackage{}, fmt.Errorf("installed plugin alias %q is ambiguous; use an exact canonical source identity or store key", selector)
	}
	return InstalledPackage{}, os.ErrNotExist
}
