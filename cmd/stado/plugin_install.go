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
		"<name>-<version>/. Idempotent — re-installing the same version is a\n" +
		"no-op advisory; a newer version installs alongside so rollback is a\n" +
		"directory swap.\n\n" +
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
		src := args[0]
		m, sig, err := plugins.LoadFromDir(src)
		if err != nil {
			return err
		}
		wasmPath := filepath.Join(src, "plugin.wasm")
		if err := plugins.VerifyWASMDigest(m.WASMSHA256, wasmPath); err != nil {
			return fmt.Errorf("install: %w", err)
		}

		// Optional TOFU path: pin the caller-provided pubkey only after it
		// matches and verifies the manifest, so failed installs do not leave
		// unintended trust-store entries behind.
		ts := plugins.NewTrustStore(cfg.StateDir())
		if pluginInstallSigner != "" {
			entry, err := ts.TrustVerified(pluginInstallSigner, m.Author, m, sig)
			if err != nil {
				return fmt.Errorf("install: --signer: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "install: pinned signer %s (author=%s)\n",
				entry.Fingerprint, m.Author)
		} else if err := ts.VerifyManifest(m, sig); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		if cfg.Plugins.CRLURL != "" {
			if err := consultCRL(cfg, m); err != nil {
				return fmt.Errorf("install: %w", err)
			}
		}

		// EP-0037 §C: validate canonical categories on every tool definition.
		// Pre-EP-0037 manifests without categories are accepted (backward compat).
		for _, td := range m.Tools {
			if err := plugins.ValidateCategories(td.Categories); err != nil {
				return fmt.Errorf("install: tool %q: %w", td.Name, err)
			}
		}

		if !filepath.IsLocal(m.Name) || !filepath.IsLocal(m.Version) ||
			strings.ContainsAny(m.Name, "/\\") || strings.ContainsAny(m.Version, "/\\") {
			return fmt.Errorf("install: plugin manifest Name or Version contains path separators or traversal (name=%q version=%q)", m.Name, m.Version)
		}

		dst := filepath.Join(cfg.StateDir(), "plugins", m.Name+"-"+m.Version)
		if info, err := os.Lstat(dst); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("install: destination is a symlink: %s", dst)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "skipped: %s v%s already installed at %s\n",
				m.Name, m.Version, dst)
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("install: stat destination %s: %w", dst, err)
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("install: copy: %w", err)
		}
		if err := verifyInstalledPluginCopy(dst, m, sig); err != nil {
			_ = workdirpath.RemoveAllNoSymlink(dst)
			return fmt.Errorf("install: verify installed copy: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "installed %s v%s at %s\n", m.Name, m.Version, dst)
		return nil
	},
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
	srcRoot, err := workdirpath.OpenRootNoSymlink(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcRoot.Close() }()

	dstParent := filepath.Dir(dst)
	dstName := filepath.Base(dst)
	if !filepath.IsLocal(dstName) || strings.ContainsAny(dstName, `/\`) || dstName == "." || dstName == ".." {
		return fmt.Errorf("invalid destination directory name: %q", dstName)
	}
	if err := workdirpath.MkdirAllUnderUserConfig(dstParent, 0o700); err != nil {
		return err
	}
	dstParentRoot, err := workdirpath.OpenRootUnderUserConfig(dstParent)
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
