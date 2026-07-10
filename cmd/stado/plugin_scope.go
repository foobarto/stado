package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

type pluginRoot struct {
	Dir   string
	Scope string
	Local bool
}

type installedPluginLocation struct {
	ID    string
	Dir   string
	Scope string
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
		ids, err := plugins.ListInstalledDirs(root.Dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s plugin dir: %w", root.Scope, err)
		}
		for _, id := range ids {
			out = append(out, installedPluginLocation{
				ID:    id,
				Dir:   filepath.Join(root.Dir, id),
				Scope: root.Scope,
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
