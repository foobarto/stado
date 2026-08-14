package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// brokerInstalledPluginVerifier independently reconstructs plugin authority
// from broker-readable install state. The orchestrator's manifest and identity
// are comparison inputs, never the trust root: a match requires an exact lock
// entry, a pinned signature, a current wasm digest, and the same manifest hash.
type brokerInstalledPluginVerifier struct {
	cfg *config.Config
}

func (v brokerInstalledPluginVerifier) VerifyArtifactPlugin(_ context.Context, requested plugins.RuntimeIdentity, supplied plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
	if v.cfg == nil {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("broker plugin verifier has no configuration")
	}
	if _, err := plugins.ParseIdentity(requested.Canonical); err != nil {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("artifact authority requires a locked installed plugin: %w", err)
	}
	suppliedDigest, err := supplied.ManifestDigest()
	if err != nil {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, err
	}
	if suppliedDigest != requested.ManifestDigest {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("supplied manifest does not match requested runtime identity")
	}

	trust := plugins.NewTrustStore(v.cfg.StateDir())
	type verified struct {
		identity plugins.RuntimeIdentity
		manifest plugins.Manifest
	}
	var matches []verified
	for _, root := range v.roots() {
		lock, err := plugins.ReadLock(root.lockPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("read %s plugin lock: %w", root.scope, err)
		}
		entry, ok := lock.Get(requested.Canonical)
		if !ok {
			continue
		}
		if entry.WASMSHA256 != supplied.WASMSHA256 {
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("locked wasm digest differs from supplied manifest")
		}
		ids, err := plugins.ListInstalledDirs(root.pluginsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("enumerate %s plugins: %w", root.scope, err)
		}
		for _, installedID := range ids {
			dir := filepath.Join(root.pluginsDir, installedID)
			manifest, signature, err := plugins.LoadFromDir(dir)
			if err != nil || manifest.WASMSHA256 != entry.WASMSHA256 {
				continue
			}
			identity, err := plugins.RuntimeIdentityFromLock(lock, *manifest)
			if err != nil || identity != requested {
				continue
			}
			if err := trust.CheckManifest(manifest, signature); err != nil {
				return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("verify installed plugin signature: %w", err)
			}
			if _, err := plugins.ReadVerifiedWASM(manifest.WASMSHA256, filepath.Join(dir, "plugin.wasm")); err != nil {
				return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("verify installed plugin wasm: %w", err)
			}
			loadedDigest, err := manifest.ManifestDigest()
			if err != nil || loadedDigest != suppliedDigest {
				continue
			}
			matches = append(matches, verified{identity: identity, manifest: *manifest})
		}
	}
	if len(matches) == 0 {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("no broker-verified installed plugin matches the requested identity")
	}
	if len(matches) > 1 {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("artifact plugin identity is ambiguous across install roots")
	}
	return matches[0].identity, matches[0].manifest, nil
}

type brokerPluginRoot struct {
	scope      string
	pluginsDir string
	lockPath   string
}

func (v brokerInstalledPluginVerifier) roots() []brokerPluginRoot {
	var roots []brokerPluginRoot
	if v.cfg.Plugins.AllowProjectPlugins && v.cfg.ProjectPluginsDir() != "" {
		roots = append(roots, brokerPluginRoot{
			scope: "project", pluginsDir: v.cfg.ProjectPluginsDir(),
			lockPath: filepath.Join(v.cfg.ProjectStadoDir(), "plugin-lock.toml"),
		})
	}
	return append(roots, brokerPluginRoot{
		scope: "global", pluginsDir: filepath.Join(v.cfg.StateDir(), "plugins"),
		lockPath: filepath.Join(v.cfg.StateDir(), "plugin-lock.toml"),
	})
}
