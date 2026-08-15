package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

type pluginRoot struct {
	Dir   string
	Scope string
	Local bool
}

// resolveManagedInstalledPackage makes installation scope explicit without
// placing scope inside the authenticated package identity. `project:` and
// `global:` are operator-facing selectors only; the remaining selector is
// still resolved by the source-keyed store and aliases still fail closed.
func resolveManagedInstalledPackage(cfg *config.Config, raw string) (plugins.InstalledPackage, pluginRoot, error) {
	selector := strings.TrimSpace(raw)
	roots := configuredPluginRoots(cfg)
	wantedScope := ""
	for _, scope := range []string{"project", "global"} {
		prefix := scope + ":"
		if strings.HasPrefix(selector, prefix) {
			wantedScope = scope
			selector = strings.TrimPrefix(selector, prefix)
			break
		}
	}
	if selector == "" {
		return plugins.InstalledPackage{}, pluginRoot{}, fmt.Errorf("plugin selector %q has no package identity", raw)
	}
	var selectedRoots []pluginRoot
	var dirs []string
	for _, root := range roots {
		if wantedScope != "" && root.Scope != wantedScope {
			continue
		}
		selectedRoots = append(selectedRoots, root)
		dirs = append(dirs, root.Dir)
	}
	if wantedScope != "" && len(selectedRoots) == 0 {
		return plugins.InstalledPackage{}, pluginRoot{}, fmt.Errorf("plugin scope %q is unavailable in this working directory", wantedScope)
	}
	pkg, err := plugins.ResolveInstalledPackage(dirs, selector)
	if err != nil {
		return plugins.InstalledPackage{}, pluginRoot{}, err
	}
	for _, root := range selectedRoots {
		if filepath.Clean(filepath.Dir(pkg.Dir)) == filepath.Clean(root.Dir) {
			return pkg, root, nil
		}
	}
	return plugins.InstalledPackage{}, pluginRoot{}, errors.New("resolved plugin is outside selected management scope")
}

type installedPluginLocation struct {
	ID      string
	Dir     string
	Scope   string
	Package plugins.InstalledPackage
}

type pluginLockTarget struct {
	Path  string
	Local bool
}

func configuredPluginRoots(cfg *config.Config) []pluginRoot {
	if cfg == nil {
		return nil
	}
	var roots []pluginRoot
	if project := cfg.ProjectPluginsDir(); project != "" {
		roots = append(roots, pluginRoot{Dir: project, Scope: "project", Local: true})
	}
	return append(roots, pluginRoot{
		Dir:   filepath.Join(cfg.StateDir(), "plugins"),
		Scope: "global",
	})
}

func listInstalledPluginLocations(cfg *config.Config) ([]installedPluginLocation, error) {
	var out []installedPluginLocation
	for _, root := range configuredPluginRoots(cfg) {
		packages, err := plugins.ListInstalledPackages(root.Dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s plugin dir: %w", root.Scope, err)
		}
		for _, pkg := range packages {
			out = append(out, installedPluginLocation{
				ID:      pkg.Record.StoreKey,
				Dir:     pkg.Dir,
				Scope:   root.Scope,
				Package: pkg,
			})
		}
	}
	return out, nil
}

func pluginLockPath(cfg *config.Config, local bool) string {
	if local && cfg != nil && cfg.ProjectStadoDir() != "" {
		return filepath.Join(cfg.ProjectStadoDir(), "plugin-lock.toml")
	}
	return filepath.Join(cfg.StateDir(), "plugin-lock.toml")
}

func pluginLockTargets(cfg *config.Config) []pluginLockTarget {
	var targets []pluginLockTarget
	if cfg != nil && cfg.ProjectStadoDir() != "" {
		targets = append(targets, pluginLockTarget{Path: pluginLockPath(cfg, true), Local: true})
	}
	return append(targets, pluginLockTarget{Path: pluginLockPath(cfg, false)})
}
