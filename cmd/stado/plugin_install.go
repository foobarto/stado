package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
)

var pluginInstallSigner string
var pluginInstallForce bool
var pluginInstallAutoload bool
var pluginInstallLocal bool
var pluginInstallTrustAnchor bool
var pluginInstallAcceptTagRewrite bool

// Keep plugin install copies aligned with the maximum signed WASM payload.
const (
	maxPluginInstallFileBytes int64 = 64 << 20
	maxPluginInstallEntries         = 4096
	maxPluginInstallDepth           = 64
)

var pluginInstallCmd = &cobra.Command{
	Use:   "install <plugin-dir>",
	Short: "Verify and install a plugin into stado's plugin directory",
	Long: "Runs the same verification as `stado plugin verify` and, on success,\n" +
		"copies the plugin directory into $XDG_DATA_HOME/stado/plugins/\n" +
		"under a host-derived source/provenance key. Idempotent — re-installing\n" +
		"the same exact package is a no-op advisory; a changed or newer package\n" +
		"installs alongside so rollback remains explicit. Pass --local to install into the discovered project's\n" +
		".stado/plugins/ directory instead; signer trust remains user-local.\n\n" +
		"When the plugin's author key isn't pinned, install fails with a hint\n" +
		"pointing at `stado plugin trust <pubkey>`. Pass --signer <pubkey> to\n" +
		"TOFU-pin inline (manifest carries only the fingerprint; stado needs\n" +
		"the full Ed25519 public key to pin). Only use --signer when you've\n" +
		"verified the signer out of band — the install's trust gate can't\n" +
		"detect a supply-chain swap on its own.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		installDir, err := pluginInstallBaseDir(cfg, pluginInstallLocal)
		if err != nil {
			return err
		}
		src := args[0]
		remote := looksLikeRemoteIdentity(src)
		if !remote && pluginInstallAcceptTagRewrite {
			return fmt.Errorf("install: --accept-tag-rewrite applies only to a remote semver identity")
		}
		var remoteID plugins.Identity
		var sourceRevision string
		var resolvedCommit string
		var anchor preparedAnchorTrust
		// EP-0039: detect remote identity (host/owner/repo@version) and fetch
		// to a local staging dir before running the install pipeline.
		if remote {
			remoteID, err = plugins.ParseIdentity(src)
			if err != nil {
				return fmt.Errorf("install: %w", err)
			}
			if pluginInstallAcceptTagRewrite && remoteID.IsCommit() {
				return fmt.Errorf("install: --accept-tag-rewrite is invalid for an immutable commit identity")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "fetching %s...\n", src)
			fetched, fetchErr := fetchRemotePlugin(src)
			if fetchErr != nil {
				return fmt.Errorf("install: %w", fetchErr)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "fetched source revision %s at commit %s to %s\n", fetched.SourceRevision, fetched.ResolvedCommit, fetched.Dir)
			src, sourceRevision, resolvedCommit = fetched.Dir, fetched.SourceRevision, fetched.ResolvedCommit
		}
		m, sig, err := plugins.LoadFromDir(src)
		if err != nil {
			return err
		}
		if err := plugins.CheckManifestHostVersion(m); err != nil {
			return fmt.Errorf("install: host compatibility: %w", err)
		}
		packageNamespace := ""
		var installRecord plugins.InstallRecord
		if remote {
			if _, err := remoteID.PackageVersion(*m); err != nil {
				return fmt.Errorf("install: %w", err)
			}
			if err := remoteID.ValidateSourceRevision(sourceRevision); err != nil {
				return fmt.Errorf("install: %w", err)
			}
			packageNamespace = remoteID.Namespace()
			if err := checkRemotePackageContinuity(cfg, pluginInstallLocal, remoteID, sourceRevision, resolvedCommit, *m, pluginInstallAcceptTagRewrite); err != nil {
				return err
			}
			installRecord, err = plugins.NewRemoteInstallRecord(remoteID, sourceRevision, resolvedCommit, *m)
			if err != nil {
				return fmt.Errorf("install: remote store identity: %w", err)
			}
		}
		// EP-0039 step 4: a remote install must be signed by the OWNER'S anchor
		// key. Enforce owner anchor trust-on-first-use AND bind it to the
		// manifest signer (m.AuthorPubkeyFpr) before the install proceeds — so a
		// manifest signed by some other globally-trusted key can't install under
		// this owner's identity. Runs after LoadFromDir so the signer fpr is known.
		if remote {
			anchor, err = prepareAnchorTrust(cmd, cfg, remoteID, m.AuthorPubkeyFpr, pluginInstallTrustAnchor)
			if err != nil {
				return err
			}
			anchorPub, parseErr := plugins.ParsePubkey(anchor.Pubkey)
			if parseErr != nil {
				return fmt.Errorf("install: verified anchor key: %w", parseErr)
			}
			if err := m.Verify(anchorPub, sig); err != nil {
				return fmt.Errorf("install: owner-anchor signature: %w", err)
			}
			if pluginInstallSigner != "" {
				provided, signerErr := plugins.ParsePubkey(strings.TrimSpace(pluginInstallSigner))
				if signerErr != nil || plugins.Fingerprint(provided) != anchor.Fingerprint {
					return fmt.Errorf("install: --signer does not match the verified owner anchor")
				}
			}
		}
		if !filepath.IsLocal(m.Name) || !filepath.IsLocal(m.Version) ||
			strings.ContainsAny(m.Name, "/\\") || strings.ContainsAny(m.Version, "/\\") {
			return fmt.Errorf("install: plugin manifest Name or Version contains path separators or traversal (name=%q version=%q)", m.Name, m.Version)
		}
		if !remote {
			installRecord, err = plugins.NewLocalInstallRecord(src, *m)
			if err != nil {
				return fmt.Errorf("install: local store identity: %w", err)
			}
			packageNamespace = installRecord.Namespace
		}
		dst := filepath.Join(installDir, installRecord.StoreKey)
		wasmPath := filepath.Join(src, "plugin.wasm")
		if err := plugins.VerifyWASMDigest(m.WASMSHA256, wasmPath); err != nil {
			return fmt.Errorf("install: %w", err)
		}

		// EP-0037 §C: validate canonical categories on every tool definition.
		// Pre-EP-0037 manifests without categories are accepted (backward compat).
		for _, td := range m.Tools {
			if err := plugins.ValidateCategories(td.Categories); err != nil {
				return fmt.Errorf("install: tool %q: %w", td.Name, err)
			}
		}
		ts := plugins.NewTrustStore(cfg.StateDir())

		// CRL is part of the verification transaction. An invalid/revoked
		// package must not leave either owner-anchor or signer trust behind.
		if cfg.Plugins.CRLURL != "" {
			if err := consultCRL(cfg, m); err != nil {
				return fmt.Errorf("install: %w", err)
			}
		}

		// Pin only after package/source, digest, signature, and CRL
		// checks. Remote owner+signer trust commits atomically in one file.
		if remote {
			entry, trustErr := ts.TrustVerifiedAnchor(anchor.Pubkey, m.Author, packageNamespace, anchor.OwnerKey, m, sig)
			if trustErr != nil {
				return fmt.Errorf("install: anchor signer: %w", trustErr)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "install: verified owner %s and signer %s atomically\n", anchor.OwnerKey, entry.Fingerprint)
		} else if pluginInstallSigner != "" {
			entry, err := ts.TrustVerifiedPackage(pluginInstallSigner, m.Author, packageNamespace, m, sig)
			if err != nil {
				return fmt.Errorf("install: --signer: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "install: pinned signer %s (author=%s)\n",
				entry.Fingerprint, m.Author)
		} else if err := ts.VerifyManifestPackage(packageNamespace, m, sig); err != nil {
			return fmt.Errorf("install: %w", err)
		}

		if info, err := os.Lstat(dst); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("install: destination is a symlink: %s", dst)
			}
			if !pluginInstallForce {
				existing, existingSig, loadErr := plugins.LoadFromDir(dst)
				if loadErr != nil {
					return fmt.Errorf("install: existing copy is unreadable (use --force after review): %w", loadErr)
				}
				existingCanonical, canonicalErr := existing.Canonical()
				if canonicalErr != nil {
					return fmt.Errorf("install: existing manifest is invalid (use --force after review): %w", canonicalErr)
				}
				incomingCanonical, canonicalErr := m.Canonical()
				if canonicalErr != nil {
					return fmt.Errorf("install: incoming manifest: %w", canonicalErr)
				}
				exactPackage := bytes.Equal(existingCanonical, incomingCanonical) && existingSig == sig
				if !exactPackage {
					return fmt.Errorf("install: source-keyed destination %s contains a different signed package; refuse replacement unless --force is used after review", dst)
				} else {
					existingRecord, recordErr := plugins.ReadInstallRecord(dst, *existing)
					if recordErr != nil {
						return fmt.Errorf("install: existing source-keyed record: %w", recordErr)
					}
					if existingRecord != installRecord {
						return fmt.Errorf("install: existing source-keyed provenance differs from incoming package")
					}
					if remote {
						if err := recordRemotePluginLock(cfg, pluginInstallLocal, remoteID, sourceRevision, resolvedCommit, *m, pluginInstallAcceptTagRewrite); err != nil {
							return err
						}
					}
					if err := plugins.WriteInstallReceipt(cfg.StateDir(), installDir, existingRecord); err != nil {
						return fmt.Errorf("install: write exact admission receipt: %w", err)
					}
					if remote && pluginInstallAcceptTagRewrite {
						if err := activateInstalledRecord(installDir, dst, existingRecord, *m); err != nil {
							_ = plugins.RemoveInstallReceipt(cfg.StateDir(), installDir, existingRecord.StoreKey)
							return fmt.Errorf("install: activate accepted rewrite: %w", err)
						}
					} else if !remote {
						if err := activateInstalledRecord(installDir, dst, existingRecord, *m); err != nil {
							_ = plugins.RemoveInstallReceipt(cfg.StateDir(), installDir, existingRecord.StoreKey)
							return fmt.Errorf("install: activate local package: %w", err)
						}
					}
					fmt.Fprintf(cmd.OutOrStdout(), "skipped: %s v%s already installed at %s\n",
						m.Name, m.Version, dst)
					return nil
				}
			} else {
				// --force: revoke live admission before removing the old bytes.
				if err := plugins.RemoveInstallReceipt(cfg.StateDir(), installDir, installRecord.StoreKey); err != nil {
					return fmt.Errorf("install: --force revoke existing receipt: %w", err)
				}
				if removeErr := workdirpath.NewUserConfigResolver().RemoveAll(dst); removeErr != nil {
					return fmt.Errorf("install: --force remove: %w", removeErr)
				}
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("install: stat destination %s: %w", dst, err)
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("install: copy: %w", err)
		}
		if err := plugins.WriteInstallRecord(dst, installRecord, *m); err != nil {
			_ = workdirpath.NewUserConfigResolver().RemoveAll(dst)
			return fmt.Errorf("install: write source-keyed install record: %w", err)
		}
		if err := verifyInstalledPluginCopy(dst, m, sig); err != nil {
			_ = workdirpath.NewUserConfigResolver().RemoveAll(dst)
			return fmt.Errorf("install: verify installed copy: %w", err)
		}
		// EP-0039: write lock file entry if this was a remote install (identity present).
		if remote {
			if err := recordRemotePluginLock(cfg, pluginInstallLocal, remoteID, sourceRevision, resolvedCommit, *m, pluginInstallAcceptTagRewrite); err != nil {
				// A remotely fetched copy without its exact source lock would be
				// rediscovered as an unrelated local-path identity. Remove the new
				// copy rather than leaving that fail-open reclassification behind.
				_ = workdirpath.NewUserConfigResolver().RemoveAll(dst)
				return err
			}
		}
		if err := plugins.WriteInstallReceipt(cfg.StateDir(), installDir, installRecord); err != nil {
			_ = workdirpath.NewUserConfigResolver().RemoveAll(dst)
			return fmt.Errorf("install: write exact admission receipt: %w", err)
		}
		if remote && pluginInstallAcceptTagRewrite {
			if err := activateInstalledRecord(installDir, dst, installRecord, *m); err != nil {
				_ = plugins.RemoveInstallReceipt(cfg.StateDir(), installDir, installRecord.StoreKey)
				_ = workdirpath.NewUserConfigResolver().RemoveAll(dst)
				return fmt.Errorf("install: activate accepted rewrite: %w", err)
			}
		} else if !remote {
			if err := activateInstalledRecord(installDir, dst, installRecord, *m); err != nil {
				_ = plugins.RemoveInstallReceipt(cfg.StateDir(), installDir, installRecord.StoreKey)
				_ = workdirpath.NewUserConfigResolver().RemoveAll(dst)
				return fmt.Errorf("install: activate local package: %w", err)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "installed %s v%s at %s\n", m.Name, m.Version, dst)
		if pluginInstallLocal && !cfg.Plugins.AllowProjectPlugins {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: project plugin loading is disabled; set [plugins] allow_project_plugins = true in %s after reviewing the project plugin\n",
				cfg.ConfigPath)
		}

		// --autoload: persist this plugin's tools into config.toml's
		// [tools].autoload list so they're loaded into every session
		// without an additional `stado tool autoload <name>` step.
		if pluginInstallAutoload && len(m.Tools) > 0 {
			names := make([]string, 0, len(m.Tools))
			for _, td := range m.Tools {
				names = append(names, td.Name)
			}
			autoloadPath := cfg.ConfigPath
			if pluginInstallLocal {
				autoloadPath = filepath.Join(cfg.ProjectStadoDir(), "config.toml")
			}
			if err := config.WriteToolsListAdd(autoloadPath, "autoload", names); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: --autoload could not update %s: %v\n", autoloadPath, err)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "autoloaded: %s\n", strings.Join(names, ", "))
			}
		}
		return nil
	},
}

func activateInstalledRecord(pluginsDir, dir string, record plugins.InstallRecord, manifest plugins.Manifest) error {
	identity, err := plugins.RuntimeIdentityForInstallRecord(pluginsDir, record, manifest)
	if err != nil {
		return err
	}
	return plugins.WriteActivePackageMarker(pluginsDir, plugins.InstalledPackage{
		Dir: dir, Record: record, Manifest: manifest, Identity: identity,
	})
}

func pluginInstallBaseDir(cfg *config.Config, local bool) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("install: config is required")
	}
	if !local {
		return filepath.Join(cfg.StateDir(), "plugins"), nil
	}
	if dir := cfg.ProjectPluginsDir(); dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("install: --local requires a project .stado directory in the current directory or an ancestor")
}

func checkRemotePackageContinuity(cfg *config.Config, local bool, id plugins.Identity, sourceRevision, resolvedCommit string, manifest plugins.Manifest, acceptRewrite bool) error {
	lock, err := plugins.ReadLock(pluginLockPath(cfg, local))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("install: read existing source lock: %w", err)
	}
	return validateRemotePackageContinuity(lock, id, sourceRevision, resolvedCommit, manifest, acceptRewrite)
}

func validateRemotePackageContinuity(lock *plugins.Lock, id plugins.Identity, sourceRevision, resolvedCommit string, manifest plugins.Manifest, acceptRewrite bool) error {
	var existing []*plugins.LockEntry
	seenStoreKeys := make(map[string]struct{})
	for index := range lock.Entries {
		entry := &lock.Entries[index]
		if entry.Identity != id.Canonical() {
			continue
		}
		if entry.StoreKey == "" {
			return fmt.Errorf("install: legacy source lock for %s has no source-keyed store key; remove it only after reviewing and reinstall the package explicitly", id.Canonical())
		}
		if _, duplicate := seenStoreKeys[entry.StoreKey]; duplicate {
			return fmt.Errorf("install: source lock contains duplicate store key %s for identity %s", entry.StoreKey, id.Canonical())
		}
		seenStoreKeys[entry.StoreKey] = struct{}{}
		existing = append(existing, entry)
	}
	if len(existing) == 0 {
		return nil
	}
	manifestDigest, err := manifest.ManifestDigest()
	if err != nil {
		return fmt.Errorf("install: canonical manifest digest: %w", err)
	}
	for _, entry := range existing {
		sameSource := entry.SourceRevision == sourceRevision && entry.ResolvedCommit == resolvedCommit
		samePackage := entry.ManifestDigest == manifestDigest && entry.WASMSHA256 == manifest.WASMSHA256
		if sameSource && samePackage {
			return nil
		}
	}
	if acceptRewrite && !id.IsCommit() {
		return nil
	}
	if id.IsCommit() {
		return fmt.Errorf("install: immutable commit identity %s conflicts with its existing source lock", id.Canonical())
	}
	entry := existing[len(existing)-1]
	sameSource := entry.SourceRevision == sourceRevision && entry.ResolvedCommit == resolvedCommit
	if !sameSource {
		return fmt.Errorf("install refused: tag rewrite detected for %s: locked %s at %s, fetched %s at %s; verify the rewrite out of band, then retry with --accept-tag-rewrite", id.Canonical(), entry.SourceRevision, entry.ResolvedCommit, sourceRevision, resolvedCommit)
	}
	return fmt.Errorf("install refused: signed package rewrite detected for %s at unchanged %s commit %s: locked manifest %s / wasm %s, fetched manifest %s / wasm %s; verify the replacement out of band, then retry with --accept-tag-rewrite", id.Canonical(), sourceRevision, resolvedCommit, entry.ManifestDigest, entry.WASMSHA256, manifestDigest, manifest.WASMSHA256)
}

func recordRemotePluginLock(cfg *config.Config, local bool, id plugins.Identity, sourceRevision, resolvedCommit string, manifest plugins.Manifest, acceptRewrite bool) error {
	lockPath := pluginLockPath(cfg, local)
	if err := workdirpath.NewUserConfigResolver().MkdirAll(filepath.Dir(lockPath), 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("install: create lock directory: %w", err)
	}
	entry, err := plugins.LockEntryFromResolvedManifest(id, sourceRevision, resolvedCommit, manifest)
	if err != nil {
		return fmt.Errorf("install: canonical manifest digest: %w", err)
	}
	if err := plugins.UpdateLock(lockPath, func(lock *plugins.Lock) error {
		// This recheck is authoritative. The earlier read avoids expensive trust
		// and copy work in the ordinary conflict case, but only validation under
		// the same cross-process lock as Add closes the concurrent rewrite race.
		if err := validateRemotePackageContinuity(lock, id, sourceRevision, resolvedCommit, manifest, acceptRewrite); err != nil {
			return err
		}
		lock.Add(entry)
		return nil
	}); err != nil {
		return fmt.Errorf("install: write lock: %w", err)
	}
	return nil
}

// copyDir copies files + regular dirs from src to dst. Symlinks and
// specials are rejected — plugin packages should be plain files.
func copyDir(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source root symlink not allowed: %s", src)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	uc := workdirpath.NewUserConfigResolver()
	srcRoot, err := uc.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcRoot.Close() }()

	dstParent := filepath.Dir(dst)
	dstName := filepath.Base(dst)
	if !filepath.IsLocal(dstName) || strings.ContainsAny(dstName, `/\`) || dstName == "." || dstName == ".." {
		return fmt.Errorf("invalid destination directory name: %q", dstName)
	}
	if err := uc.MkdirAll(dstParent, 0o700); err != nil {
		return err
	}
	dstParentRoot, err := uc.OpenRoot(dstParent)
	if err != nil {
		return err
	}
	defer func() { _ = dstParentRoot.Close() }()
	if info, err := dstParentRoot.Lstat(dstName); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination symlink not allowed: %s", dst)
		}
		return fmt.Errorf("destination already exists: %s", dst)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := dstParentRoot.Mkdir(dstName, 0o700); err != nil {
		return err
	}
	dstRoot, err := dstParentRoot.OpenRoot(dstName)
	if err != nil {
		_ = dstParentRoot.RemoveAll(dstName)
		return err
	}
	err = copyRootDir(srcRoot, dstRoot, ".", &pluginInstallCopyState{})
	closeErr := dstRoot.Close()
	if err != nil {
		_ = dstParentRoot.RemoveAll(dstName)
		return err
	}
	if closeErr != nil {
		_ = dstParentRoot.RemoveAll(dstName)
		return closeErr
	}
	return nil
}

type pluginInstallCopyState struct {
	entries int
}

func copyRootDir(srcRoot, dstRoot *os.Root, rel string, state *pluginInstallCopyState) error {
	if state == nil {
		state = &pluginInstallCopyState{}
	}
	return copyRootDirDepth(srcRoot, dstRoot, rel, state, 0)
}

func copyRootDirDepth(srcRoot, dstRoot *os.Root, rel string, state *pluginInstallCopyState, depth int) error {
	if depth > maxPluginInstallDepth {
		return fmt.Errorf("plugin package nesting exceeds %d directories: %s", maxPluginInstallDepth, rel)
	}
	dir, err := srcRoot.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, e := range entries {
			state.entries++
			if state.entries > maxPluginInstallEntries {
				return fmt.Errorf("plugin package contains more than %d entries", maxPluginInstallEntries)
			}
			name := e.Name()
			// Never copy signing seeds (private keys) or the dev-only .stado/
			// dir into the installed package. `plugin use-dev` writes
			// .stado/dev.seed next to the source; copying it would ship a
			// private signing key into the install tree.
			if strings.HasSuffix(name, ".seed") || name == ".stado" {
				continue
			}
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return fmt.Errorf("invalid plugin package entry name: %q", name)
			}
			childRel := name
			if rel != "." {
				childRel = filepath.Join(rel, name)
			}
			info, err := srcRoot.Lstat(childRel)
			if err != nil {
				return err
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				return fmt.Errorf("symlink not allowed: %s", childRel)
			case info.IsDir():
				if err := dstRoot.Mkdir(childRel, 0o700); err != nil {
					return err
				}
				if err := copyRootDirDepth(srcRoot, dstRoot, childRel, state, depth+1); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if err := copyPluginFile(srcRoot, dstRoot, childRel, installedPluginFileMode(info.Mode())); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported file mode for %s: %v", childRel, info.Mode())
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func copyPluginFile(srcRoot, dstRoot *os.Root, rel string, mode os.FileMode) error {
	sourceInfo, err := srcRoot.Lstat(rel)
	if err != nil {
		return err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink not allowed: %s", rel)
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", rel)
	}
	in, err := srcRoot.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", rel)
	}
	if !os.SameFile(sourceInfo, openedInfo) {
		return fmt.Errorf("source file changed while opening: %s", rel)
	}
	if openedInfo.Size() > maxPluginInstallFileBytes {
		return fmt.Errorf("plugin package file exceeds %d bytes: %s", maxPluginInstallFileBytes, rel)
	}
	out, err := dstRoot.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := copyAndCloseFileLimited(out, in, maxPluginInstallFileBytes); err != nil {
		_ = dstRoot.Remove(rel)
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

func verifyInstalledPluginCopy(dst string, want *plugins.Manifest, sig string) error {
	got, gotSig, err := plugins.LoadFromDir(dst)
	if err != nil {
		return err
	}
	wantCanonical, err := want.Canonical()
	if err != nil {
		return err
	}
	gotCanonical, err := got.Canonical()
	if err != nil {
		return err
	}
	if !bytes.Equal(gotCanonical, wantCanonical) || gotSig != sig {
		return fmt.Errorf("copied manifest/signature changed during install")
	}
	if err := plugins.VerifyWASMDigest(want.WASMSHA256, filepath.Join(dst, "plugin.wasm")); err != nil {
		return err
	}
	return nil
}

func installedPluginFileMode(mode os.FileMode) os.FileMode {
	perm := mode.Perm() & 0o700
	perm |= 0o600
	if mode.Perm()&0o111 != 0 {
		perm |= 0o100
	}
	return perm
}
