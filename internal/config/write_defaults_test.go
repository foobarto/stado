package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDefaultsCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := WriteDefaults(path, "lmstudio", "qwen"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`[defaults]`, `provider = "lmstudio"`, `model = "qwen"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteDefaultsPreservesUnspecifiedProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nprovider = \"anthropic\"\nmodel = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteDefaults(path, "", "new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`provider = "anthropic"`, `model = "new"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteTUIThinkingDisplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nprovider = \"anthropic\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteTUIThinkingDisplay(path, "hide"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`provider = "anthropic"`, `[tui]`, `thinking_display = "hide"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteTUITheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := WriteTUITheme(path, "stado-rose"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`[tui]`, `theme = "stado-rose"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteDefaultsRejectsConfigSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(outside, []byte("[defaults]\nprovider = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := WriteDefaults(path, "openai", "gpt-test"); err == nil {
		t.Fatal("WriteDefaults should reject symlink escape")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "openai") {
		t.Fatalf("outside config was modified:\n%s", data)
	}
}
