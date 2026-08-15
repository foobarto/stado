package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
)

// brokerInstalledPluginVerifier independently reconstructs plugin authority
// from broker-readable install state. The orchestrator's manifest and identity
// are comparison inputs, never the trust root: a match requires an exact
// source identity (remote lock or source-bound local install), a package-scoped
// pinned signature, a current wasm digest, and the same manifest hash.
type brokerInstalledPluginVerifier struct {
	cfg *config.Config
}

func (v brokerInstalledPluginVerifier) VerifyArtifactPlugin(ctx context.Context, requested plugins.RuntimeIdentity, supplied plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
	if v.cfg == nil {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("broker plugin verifier has no configuration")
	}
	if err := requested.ValidateManifest(supplied); err != nil {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, errors.New("supplied manifest does not match requested runtime identity")
	}
	suppliedDigest := requested.ManifestDigest

	type verified struct {
		identity plugins.RuntimeIdentity
		manifest plugins.Manifest
	}
	var matches []verified
	for _, root := range v.roots() {
		packages, err := plugins.ListInstalledPackages(root.pluginsDir)
		if err != nil {
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("enumerate %s plugins: %w", root.scope, err)
		}
		for _, pkg := range packages {
			dir, manifest, signature := pkg.Dir, &pkg.Manifest, pkg.Signature
			loadedDigest, err := manifest.ManifestDigest()
			if err != nil || loadedDigest != suppliedDigest {
				continue
			}
			identity := pkg.Identity
			if identity != requested {
				continue
			}
			if err := runtime.VerifyInstalledPlugin(ctx, v.cfg, dir, manifest, signature); err != nil {
				return plugins.RuntimeIdentity{}, plugins.Manifest{}, fmt.Errorf("verify installed plugin signature: %w", err)
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
}

func (v brokerInstalledPluginVerifier) roots() []brokerPluginRoot {
	var roots []brokerPluginRoot
	if v.cfg.Plugins.AllowProjectPlugins && v.cfg.ProjectPluginsDir() != "" {
		roots = append(roots, brokerPluginRoot{
			scope: "project", pluginsDir: v.cfg.ProjectPluginsDir(),
		})
	}
	return append(roots, brokerPluginRoot{
		scope: "global", pluginsDir: filepath.Join(v.cfg.StateDir(), "plugins"),
	})
}
