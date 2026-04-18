package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const appName = "stado"

// Config is the top-level stado configuration.
//
// Phase 0 scaffold: legacy [providers.*], [context], [embeddings] sections are
// gone; [inference], [sandbox], [git], [otel], [acp], [plugins] are placeholders
// that later phases fill in (see PLAN.md).
type Config struct {
	ConfigPath string `koanf:"-"`

	Defaults  Defaults  `koanf:"defaults"`
	Approvals Approvals `koanf:"approvals"`
	MCP       MCP       `koanf:"mcp"`

	Inference Inference `koanf:"inference"`
	Sandbox   Sandbox   `koanf:"sandbox"`
	Git       Git       `koanf:"git"`
	OTel      OTel      `koanf:"otel"`
	ACP       ACP       `koanf:"acp"`
	Plugins   Plugins   `koanf:"plugins"`
}

type Defaults struct {
	Provider string `koanf:"provider"`
	Model    string `koanf:"model"`
}

type Approvals struct {
	Mode      string   `koanf:"mode"`
	Allowlist []string `koanf:"allowlist"`
}

type MCP struct {
	ConfigPath string `koanf:"config_path"`
}

// Inference is Phase 1's [inference] section: presets for OAI-compat endpoints
// plus per-provider settings. Filled in with Phase 1.
type Inference struct {
	Presets map[string]InferencePreset `koanf:"presets"`
}

type InferencePreset struct {
	Endpoint string `koanf:"endpoint"`
}

// Sandbox is Phase 3's [sandbox] section — placeholder.
type Sandbox struct{}

// Git is Phase 2's [git] section — sidecar paths, author identity.
type Git struct{}

// OTel is Phase 6's [otel] section. Mirrors telemetry.Config shape so
// internal/telemetry can cast this straight into its config type.
type OTel struct {
	Enabled     bool              `koanf:"enabled"`
	Endpoint    string            `koanf:"endpoint"`
	Protocol    string            `koanf:"protocol"`
	Insecure    bool              `koanf:"insecure"`
	Headers     map[string]string `koanf:"headers"`
	SampleRate  float64           `koanf:"sample_rate"`
	ServiceName string            `koanf:"service_name"`
}

// ACP is Phase 8's [acp] section.
type ACP struct{}

// Plugins is Phase 7's [plugins] section — trusted signer fingerprints, CRL URL.
type Plugins struct{}

func Load() (*Config, error) {
	k := koanf.New(".")

	configPath := defaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	if _, err := os.Stat(configPath); err == nil {
		if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
	}

	if err := k.Load(env.Provider("STADO_", ".", func(s string) string {
		key := strings.ToLower(strings.TrimPrefix(s, "STADO_"))
		return strings.ReplaceAll(key, "_", ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	cfg.ConfigPath = configPath

	if cfg.Defaults.Provider == "" {
		cfg.Defaults.Provider = "anthropic"
	}
	if cfg.Defaults.Model == "" {
		cfg.Defaults.Model = "claude-sonnet-4-5"
	}
	if cfg.Approvals.Mode == "" {
		cfg.Approvals.Mode = "prompt"
	}

	return &cfg, nil
}

func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName, "config.toml")
}

func (c *Config) StateDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", appName)
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
