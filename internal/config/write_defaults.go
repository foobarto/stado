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
	return updateConfig(configPath, func(tree *toml.Tree) {
		if provider = strings.TrimSpace(provider); provider != "" {
			tree.SetPath([]string{"defaults", "provider"}, provider)
		}
		if model = strings.TrimSpace(model); model != "" {
			tree.SetPath([]string{"defaults", "model"}, model)
		}
	})
}

// WriteTUIThinkingDisplay updates [tui].thinking_display in config.toml.
func WriteTUIThinkingDisplay(configPath, mode string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	mode = normalizeThinkingDisplay(mode)
	return updateConfig(configPath, func(tree *toml.Tree) {
		tree.SetPath([]string{"tui", "thinking_display"}, mode)
	})
}

// WriteTUITheme updates [tui].theme in config.toml.
func WriteTUITheme(configPath, themeID string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	themeID = strings.TrimSpace(themeID)
	if themeID == "" {
		return fmt.Errorf("theme id is empty")
	}
	return updateConfig(configPath, func(tree *toml.Tree) {
		tree.SetPath([]string{"tui", "theme"}, themeID)
	})
}

func updateConfig(configPath string, mutate func(*toml.Tree)) error {
	dir := filepath.Dir(configPath)
	name := filepath.Base(configPath)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid config path %q", configPath)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open config dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	var tree *toml.Tree
	data, err := root.ReadFile(name)
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

	mutate(tree)

	out, err := tree.ToTomlString()
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := root.WriteFile(name, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
