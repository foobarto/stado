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

const maxConfigFileBytes int64 = 1 << 20

// WriteInferencePreset adds (or overwrites) [inference.presets.<name>]
// in config.toml with the given endpoint + api_key_env. Used by
// `stado config providers setup --write` so users don't have to
// hand-edit TOML for known providers.
func WriteInferencePreset(configPath, name, endpoint, apiKeyEnv string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("preset name is empty")
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("preset endpoint is empty")
	}
	return updateConfig(configPath, func(tree *toml.Tree) {
		tree.SetPath([]string{"inference", "presets", name, "endpoint"}, endpoint)
		if apiKeyEnv = strings.TrimSpace(apiKeyEnv); apiKeyEnv != "" {
			tree.SetPath([]string{"inference", "presets", name, "api_key_env"}, apiKeyEnv)
		}
	})
}

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

// WriteToolsListAdd appends entries to [tools].<key> ("enabled" /
// "disabled" / "autoload"). Existing entries in the list are preserved.
// Duplicates are de-duped. The list is created when absent. Empty entries
// are ignored. EP-0037 §F — `stado tool {enable,disable,autoload}` config
// persistence.
func WriteToolsListAdd(configPath, key string, entries []string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	if !isToolsListKey(key) {
		return fmt.Errorf("unknown [tools] list key %q (want enabled / disabled / autoload)", key)
	}
	add := dedupeNonEmpty(entries)
	if len(add) == 0 {
		return fmt.Errorf("no entries to add")
	}
	return updateConfig(configPath, func(tree *toml.Tree) {
		existing := readStringList(tree, []string{"tools", key})
		merged := dedupeNonEmpty(append(existing, add...))
		tree.SetPath([]string{"tools", key}, anySliceFromStrings(merged))
	})
}

// WriteToolsListRemove removes entries from [tools].<key>. Non-present
// entries are silently ignored. The list is left empty (not deleted) when
// no entries remain — keeps the section visible for inspection.
func WriteToolsListRemove(configPath, key string, entries []string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	if !isToolsListKey(key) {
		return fmt.Errorf("unknown [tools] list key %q (want enabled / disabled / autoload)", key)
	}
	rm := dedupeNonEmpty(entries)
	if len(rm) == 0 {
		return fmt.Errorf("no entries to remove")
	}
	rmSet := make(map[string]bool, len(rm))
	for _, e := range rm {
		rmSet[e] = true
	}
	return updateConfig(configPath, func(tree *toml.Tree) {
		existing := readStringList(tree, []string{"tools", key})
		kept := make([]string, 0, len(existing))
		for _, e := range existing {
			if !rmSet[e] {
				kept = append(kept, e)
			}
		}
		tree.SetPath([]string{"tools", key}, anySliceFromStrings(kept))
	})
}

func isToolsListKey(k string) bool {
	switch k {
	case "enabled", "disabled", "autoload":
		return true
	}
	return false
}

func readStringList(tree *toml.Tree, path []string) []string {
	v := tree.GetPath(path)
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func anySliceFromStrings(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
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
	if err := workdirpath.MkdirAllUnderUserConfig(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	root, err := workdirpath.OpenRootUnderUserConfig(dir)
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
	if err := workdirpath.MkdirAllUnderUserConfig(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	root, err := workdirpath.OpenRootUnderUserConfig(dir)
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
		data, err := workdirpath.ReadRootRegularFileLimited(root, name, maxConfigFileBytes)
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
