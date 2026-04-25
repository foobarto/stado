package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui/theme"
)

func TestLoadRenderer_UsesTemplateOverlay(t *testing.T) {
	cfgDir := t.TempDir()
	overlayDir := filepath.Join(cfgDir, "templates")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(overlayDir, "message_assistant.tmpl"),
		[]byte("OVERRIDE {{ .Body }}"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	rnd, err := loadRenderer(&config.Config{
		ConfigPath: filepath.Join(cfgDir, "config.toml"),
	}, theme.Default())
	if err != nil {
		t.Fatal(err)
	}

	out, err := rnd.Exec("message_assistant", map[string]any{
		"Model": "test-model",
		"Body":  "hello",
		"Width": 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "OVERRIDE hello\n" {
		t.Fatalf("rendered assistant template = %q, want %q", out, "OVERRIDE hello\n")
	}
}

func TestLoadTheme_ConfigThemeBeatsThemeTOML(t *testing.T) {
	cfgDir := t.TempDir()
	data, ok := theme.BuiltinTOML("stado-rose")
	if !ok {
		t.Fatal("stado-rose theme missing")
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "theme.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	th, err := loadTheme(&config.Config{
		ConfigPath: filepath.Join(cfgDir, "config.toml"),
		TUI:        config.TUI{Theme: "stado-light"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "stado-light" {
		t.Fatalf("theme = %q, want stado-light", th.Name)
	}
}
