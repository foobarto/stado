package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
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

// WriteTemplate writes a complete config file template. Without force, it
// creates the final path exclusively. With force, it atomically replaces only a
// regular final file. Symlink and non-regular final paths are always rejected.
func WriteTemplate(configPath string, data []byte, force bool) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	dir := filepath.Dir(configPath)
	name := filepath.Base(configPath)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid config path %q", configPath)
	}
	if err := workdirpath.MkdirAllNoSymlink(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return fmt.Errorf("open config dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(name)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("config file is a symlink: %s", name)
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("config file is not regular: %s", name)
	case err == nil && !force:
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", configPath)
	case err == nil:
		return writeConfigFileAtomic(root, name, data, 0o600)
	case os.IsNotExist(err):
		return writeConfigFileExclusive(root, name, data, 0o600)
	default:
		return fmt.Errorf("read config: %w", err)
	}
}

func updateConfig(configPath string, mutate func(*toml.Tree)) error {
	dir := filepath.Dir(configPath)
	name := filepath.Base(configPath)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid config path %q", configPath)
	}
	if err := workdirpath.MkdirAllNoSymlink(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return fmt.Errorf("open config dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	var tree *toml.Tree
	info, err := root.Lstat(name)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("config file is a symlink: %s", name)
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("config file is not regular: %s", name)
	case err == nil:
		data, err := root.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read config: %w", err)
		}
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
	if err := writeConfigFileAtomic(root, name, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func writeConfigFileExclusive(root *os.Root, name string, data []byte, perm os.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	keepFile := false
	defer func() {
		if !keepFile {
			_ = root.Remove(name)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	keepFile = true
	return nil
}

func writeConfigFileAtomic(root *os.Root, name string, data []byte, perm os.FileMode) error {
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("config file is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("config file is not regular: %s", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	tmp := "." + name + "." + uuid.NewString() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmp)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}
