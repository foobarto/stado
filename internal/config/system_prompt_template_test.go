package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/instructions"
)

func TestMigratableDefaultSystemPromptTemplateSHA256(t *testing.T) {
	for _, sum := range []string{
		legacyDefaultSystemPromptTemplateSHA256,
		previousDefaultSystemPromptTemplateSHA256,
	} {
		if !isMigratableDefaultSystemPromptTemplateSHA256(sum) {
			t.Fatalf("default system prompt digest %q is not migratable", sum)
		}
	}
	if isMigratableDefaultSystemPromptTemplateSHA256("custom") {
		t.Fatal("custom system prompt digest should not be migratable")
	}
	if isLegacyDefaultSystemPromptTemplate([]byte(instructions.DefaultSystemPromptTemplate)) {
		t.Fatal("current default system prompt should not immediately migrate")
	}
}

func TestLoadUpdatesImmediatelyPreviousDefaultSystemPromptTemplate(t *testing.T) {
	fixture, err := os.ReadFile("testdata/system-prompt-before-task-deferral.md")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(fixture)
	if got := fmt.Sprintf("%x", sum[:]); got != previousDefaultSystemPromptTemplateSHA256 {
		t.Fatalf("previous default fixture digest = %s, want %s", got, previousDefaultSystemPromptTemplateSHA256)
	}

	t.Run("untouched default migrates", func(t *testing.T) {
		promptPath := writeSystemPromptFixture(t, fixture)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.Agent.SystemPromptTemplate != instructions.DefaultSystemPromptTemplate {
			t.Fatal("immediately previous generated prompt was not updated")
		}
		got, err := os.ReadFile(promptPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != instructions.DefaultSystemPromptTemplate {
			t.Fatal("migrated prompt file does not contain the current default")
		}
	})

	t.Run("modified default remains custom", func(t *testing.T) {
		custom := bytes.Replace(fixture, []byte("You are stado"), []byte("You are Stado"), 1)
		promptPath := writeSystemPromptFixture(t, custom)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.Agent.SystemPromptTemplate != string(custom) {
			t.Fatal("one-byte-modified previous prompt was overwritten")
		}
		got, err := os.ReadFile(promptPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, custom) {
			t.Fatal("custom prompt file changed during load")
		}
	})
}

func writeSystemPromptFixture(t *testing.T, body []byte) string {
	t.Helper()
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(configDir, "system-prompt.md")
	if err := os.WriteFile(promptPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return promptPath
}
