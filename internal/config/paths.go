package config

// Path resolution helpers — config dir, state dir, project .stado/
// discovery, worktree root, sidecar repo location. Centralised here
// rather than in config.go so the loader file stays focused on
// koanf wiring + struct definitions.

import (
	"os"
	"path/filepath"
	"strings"
)

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// DefaultConfigPath returns the operator-level config file location:
// $XDG_CONFIG_HOME/stado/config.toml, falling back to
// ~/.config/stado/config.toml. Mirrors what config.Load() reads when
// no project-local override is present. Exported so CLI subcommands
// like `stado tool enable --global` can target the same file.
func DefaultConfigPath() string { return defaultConfigPath() }

// ConfigDir returns the directory containing config.toml — the
// per-user stado config dir. Used by `personas`, `skills`, etc.
// for resolution paths under <ConfigDir>/personas/, etc.
func ConfigDir() string {
	return filepath.Dir(defaultConfigPath())
}

func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName, "config.toml")
}

// findProjectStadoDir walks from start upward looking for a directory
// that contains a `.stado/` subdirectory. Returns the `.stado/` path
// when found, or "" when nothing is found up to the filesystem root.
// EP-0035.
func findProjectStadoDir(start string) string {
	abs, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, ".stado")
		info, err := os.Lstat(candidate)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func (c *Config) StateDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", appName)
}

// ProjectStadoDir returns the absolute path of the .stado/ directory
// found by walking up from cwd at Load() time, or "" when none exists.
// EP-0035.
func (c *Config) ProjectStadoDir() string { return c.projectStadoDir }

// ProjectPluginsDir returns the per-project plugin search directory
// (.stado/plugins/) when a .stado/ directory was found, or "" when none
// exists. The directory may not exist yet — callers should check before
// listing it. EP-0035.
func (c *Config) ProjectPluginsDir() string {
	if c.projectStadoDir == "" {
		return ""
	}
	return filepath.Join(c.projectStadoDir, "plugins")
}

// AllPluginDirs returns all directories to search for installed plugins,
// in priority order: project-local first (so project plugins shadow
// global ones with the same name+version), then global. Empty entries
// are filtered out. Callers should search all returned dirs and use the
// first match. EP-0035.
func (c *Config) AllPluginDirs() []string {
	global := filepath.Join(c.StateDir(), "plugins")
	project := c.ProjectPluginsDir()
	if project == "" {
		return []string{global}
	}
	return []string{project, global}
}

// WorktreeDir is the root under which per-session worktrees live. Uses
// XDG_STATE_HOME (volatile user state) per PLAN.md §2.1.
func (c *Config) WorktreeDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "worktrees")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", appName, "worktrees")
}

// SidecarPath returns the bare-repo path for the user repo rooted at
// userRepoRoot (or cwd if empty). Filename is stable-hashed via RepoID.
func (c *Config) SidecarPath(userRepoRoot, repoID string) string {
	return filepath.Join(c.StateDir(), "sessions", repoID+".git")
}
