package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/theme"
)

func TestThemeSlashOpensPicker(t *testing.T) {
	m := scenarioModel(t)

	_ = m.handleSlash("/theme")

	if !m.themePick.Visible {
		t.Fatal("/theme should open the theme picker")
	}
	if len(m.themePick.Items) < 3 {
		t.Fatalf("theme picker items = %d, want at least 3", len(m.themePick.Items))
	}
}

func TestThemePickerKeybindOpensPicker(t *testing.T) {
	m := scenarioModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	if !m.themePick.Visible {
		t.Fatal("ctrl+x t should open the theme picker")
	}
}

func TestApplyNamedThemePersistsSelection(t *testing.T) {
	defer theme.Apply(theme.Default())

	m := scenarioModel(t)
	dir := t.TempDir()
	m.cfg = &config.Config{ConfigPath: filepath.Join(dir, "config.toml")}
	m.blocks = []block{{kind: "assistant", body: "cached", cachedOut: "stale"}}

	if err := m.applyNamedTheme("stado-light"); err != nil {
		t.Fatal(err)
	}

	if got := m.theme.Name; got != "stado-light" {
		t.Fatalf("theme = %q, want stado-light", got)
	}
	if m.renderer.Theme().Name != "stado-light" {
		t.Fatalf("renderer theme = %q, want stado-light", m.renderer.Theme().Name)
	}
	if m.blocks[0].cachedOut == "stale" {
		t.Fatal("theme switch should replace stale rendered block cache")
	}
	data, err := os.ReadFile(filepath.Join(dir, "theme.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name = "stado-light"`) {
		t.Fatalf("persisted theme.toml missing light theme name: %s", data)
	}
}

func TestThemeCommandInPalette(t *testing.T) {
	m := scenarioModel(t)
	m.slash.Open()

	for _, r := range "theme" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.themePick.Visible {
		t.Fatal("selecting /theme from command palette should open theme picker")
	}
	if !m.keys.Matches(tea.KeyMsg{Type: tea.KeyCtrlP}, keys.CommandList) {
		t.Fatal("sanity: command palette keybinding should remain ctrl+p")
	}
}
