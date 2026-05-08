package config

// System-prompt-template lifecycle. The template lives at
// <config-dir>/system-prompt.md by default (or wherever
// [agent].system_prompt_path points). On first start, stado writes
// a default template; on subsequent starts it loads + validates the
// existing one. The legacy SHA-256 fingerprint catches operators
// who haven't customised since v0.x and rolls them onto the current
// default — a one-shot migration that avoids scaring users with
// validation errors on a file they never edited.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/workdirpath"
)

const defaultSystemPromptFilename = "system-prompt.md"
const legacyDefaultSystemPromptTemplateSHA256 = "e712fed3c1f394afa61cb4f078fe3bde7acee8a902e75ab5914753aafcf04188"

func (c *Config) loadSystemPromptTemplate() error {
	explicitPath := strings.TrimSpace(c.Agent.SystemPromptPath) != ""
	if !explicitPath {
		c.Agent.SystemPromptPath = filepath.Join(filepath.Dir(c.ConfigPath), defaultSystemPromptFilename)
		if err := ensureDefaultSystemPromptTemplate(c.Agent.SystemPromptPath); err != nil {
			return err
		}
	} else {
		c.Agent.SystemPromptPath = expandHome(c.Agent.SystemPromptPath)
	}
	var body []byte
	var err error
	body, err = workdirpath.NewUserConfigResolver().ReadFileLimited(c.Agent.SystemPromptPath, maxSystemPromptTemplateBytes)
	if err != nil {
		return fmt.Errorf("load [agent].system_prompt_path %s: %w", c.Agent.SystemPromptPath, err)
	}
	if err := instructions.ValidateSystemPromptTemplate(string(body)); err != nil {
		return fmt.Errorf("validate [agent].system_prompt_path %s: %w", c.Agent.SystemPromptPath, err)
	}
	c.Agent.SystemPromptTemplate = string(body)
	return nil
}

func ensureDefaultSystemPromptTemplate(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("default system prompt template is a symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("default system prompt template is not a regular file: %s", path)
		}
		data, err := workdirpath.NewUserConfigResolver().ReadFileLimited(path, maxSystemPromptTemplateBytes)
		if err != nil {
			return fmt.Errorf("read default system prompt template: %w", err)
		}
		if isLegacyDefaultSystemPromptTemplate(data) {
			if err := replaceDefaultSystemPromptTemplate(path); err != nil {
				return fmt.Errorf("update default system prompt template: %w", err)
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat default system prompt template: %w", err)
	}
	if err := workdirpath.NewUserConfigResolver().MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create system prompt template dir: %w", err)
	}
	if err := createDefaultSystemPromptTemplate(path); err != nil {
		return fmt.Errorf("write default system prompt template: %w", err)
	}
	return nil
}

func createDefaultSystemPromptTemplate(path string) error {
	root, name, err := systemPromptTemplateRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := writeSystemPromptTemplateFile(f); err != nil {
		_ = f.Close()
		_ = root.Remove(name)
		return err
	}
	return nil
}

func replaceDefaultSystemPromptTemplate(path string) error {
	root, name, err := systemPromptTemplateRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmpName := "." + name + "." + uuid.NewString() + ".tmp"
	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmpName)
		}
	}()
	if err := writeSystemPromptTemplateFile(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

func writeSystemPromptTemplateFile(f *os.File) error {
	body := []byte(instructions.DefaultSystemPromptTemplate)
	n, err := f.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func systemPromptTemplateRoot(path string) (*os.Root, string, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", fmt.Errorf("invalid system prompt template path: %s", path)
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

func isLegacyDefaultSystemPromptTemplate(data []byte) bool {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]) == legacyDefaultSystemPromptTemplateSHA256
}
