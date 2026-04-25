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
