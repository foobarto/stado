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

type Config struct {
	ConfigPath string `koanf:"-"`

	Defaults   Defaults            `koanf:"defaults"`
	Providers  map[string]Provider `koanf:"providers"`
	Approvals  Approvals           `koanf:"approvals"`
	Context    Context             `koanf:"context"`
	Embeddings Embeddings          `koanf:"embeddings"`
	MCP        MCP                 `koanf:"mcp"`
}

type Defaults struct {
	Provider string `koanf:"provider"`
	Model    string `koanf:"model"`
}

type Provider struct {
	Kind        string `koanf:"kind"`
	BaseURL     string `koanf:"base_url"`
	APIKeyEnv   string `koanf:"api_key_env"`
	Profile     string `koanf:"profile"`
	Mode        string `koanf:"mode"`
	Command     string `koanf:"command"`
	OneShotArgs []string `koanf:"one_shot_args"`
	ResumeArg   string `koanf:"resume_arg"`
	OutputFormat string `koanf:"output_format"`
	SessionIDRegex string `koanf:"session_id_regex"`
	Env         map[string]string `koanf:"env"`
}

type Approvals struct {
	Mode      string   `koanf:"mode"`
	Allowlist []string `koanf:"allowlist"`
}

type Context struct {
	Enabled    bool `koanf:"enabled"`
	Lexical    bool `koanf:"lexical"`
	Symbols    bool `koanf:"symbols"`
	Embeddings bool `koanf:"embeddings"`
}

type Embeddings struct {
	Provider string `koanf:"provider"`
	Model    string `koanf:"model"`
}

type MCP struct {
	ConfigPath string `koanf:"config_path"`
}

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
	if !cfg.Context.Enabled {
		cfg.Context.Enabled = true
		cfg.Context.Lexical = true
		cfg.Context.Symbols = true
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
