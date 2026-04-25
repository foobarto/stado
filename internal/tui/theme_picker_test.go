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

func TestThemePickerIncludesCurrentCustomTheme(t *testing.T) {
	m := scenarioModel(t)
	custom := theme.Default()
	custom.Name = "my-custom"
	m.theme = custom

	items := m.themePickerItems()
	found := false
	for _, it := range items {
		if it.ID == "my-custom" {
			found = true
			if !it.Current || it.Mode != "custom" || !strings.Contains(it.Desc, "theme.toml") {
				t.Fatalf("custom theme item = %+v", it)
			}
		}
	}
	if !found {
		t.Fatalf("custom theme row missing: %+v", items)
	}
}

func TestThemePickerSelectingCurrentCustomThemeIsNoop(t *testing.T) {
	m := scenarioModel(t)
	custom := theme.Default()
	custom.Name = "my-custom"
	m.theme = custom
	m.openThemePicker()

	if sel := m.themePick.Selected(); sel == nil || sel.ID != "my-custom" {
		t.Fatalf("selected = %+v, want current custom theme", sel)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.themePick.Visible {
		t.Fatal("enter should close theme picker")
	}
	if len(m.blocks) != 0 {
		t.Fatalf("selecting current custom theme should not append errors: %+v", m.blocks)
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

func TestThemeCommandSupportsLightDarkAliases(t *testing.T) {
	defer theme.Apply(theme.Default())

	m := scenarioModel(t)

	_ = m.handleSlash("/theme light")
	if got := m.theme.Name; got != "stado-light" {
		t.Fatalf("/theme light selected %q, want stado-light", got)
	}

	_ = m.handleSlash("/theme dark")
	if got := m.theme.Name; got != "stado-dark" {
		t.Fatalf("/theme dark selected %q, want stado-dark", got)
	}
}

func TestThemeCommandToggleSwitchesLightAndDark(t *testing.T) {
	defer theme.Apply(theme.Default())

	m := scenarioModel(t)
	if err := m.applyNamedTheme("stado-light"); err != nil {
		t.Fatal(err)
	}

	_ = m.handleSlash("/theme toggle")
	if got := m.theme.Name; got != "stado-dark" {
		t.Fatalf("toggle from light selected %q, want stado-dark", got)
	}

	_ = m.handleSlash("/theme toggle")
	if got := m.theme.Name; got != "stado-light" {
		t.Fatalf("toggle from dark selected %q, want stado-light", got)
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
