package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml"
)

// WriteDefaults updates [defaults] in config.toml. Empty values are ignored so
// callers can persist only the setting they know.
func WriteDefaults(configPath, provider, model string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}

	var tree *toml.Tree
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		tree, err = toml.LoadBytes(data)
		if err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	case os.IsNotExist(err):
		tree, err = toml.TreeFromMap(map[string]interface{}{})
		if err != nil {
			return fmt.Errorf("create config tree: %w", err)
		}
	default:
		return fmt.Errorf("read config: %w", err)
	}

	if provider = strings.TrimSpace(provider); provider != "" {
		tree.SetPath([]string{"defaults", "provider"}, provider)
	}
	if model = strings.TrimSpace(model); model != "" {
		tree.SetPath([]string{"defaults", "model"}, model)
	}

	out, err := tree.ToTomlString()
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
