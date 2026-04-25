package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/tui/themepicker"
)

func (m *Model) openThemePicker() {
	m.themePick.Open(m.themePickerItems(), m.currentThemeID())
}

func (m *Model) themePickerItems() []themepicker.Item {
	catalog := theme.Catalog()
	items := make([]themepicker.Item, 0, len(catalog))
	current := m.currentThemeID()
	currentBundled := false
	for _, entry := range catalog {
		if entry.ID == current {
			currentBundled = true
		}
		items = append(items, themepicker.Item{
			ID:      entry.ID,
			Name:    entry.Name,
			Mode:    entry.Mode,
			Desc:    entry.Description,
			Current: entry.ID == current,
		})
	}
	if current != "" && !currentBundled {
		items = append(items, themepicker.Item{
			ID:      current,
			Name:    current,
			Mode:    "custom",
			Desc:    "loaded theme.toml",
			Current: true,
		})
	}
	return items
}

func (m *Model) currentThemeID() string {
	if m == nil || m.theme == nil {
		return ""
	}
	return strings.TrimSpace(m.theme.Name)
}

func (m *Model) applyThemeSelection(selection string) error {
	id, err := m.resolveThemeSelection(selection)
	if err != nil {
		return err
	}
	return m.applyNamedTheme(id)
}

func (m *Model) resolveThemeSelection(selection string) (string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return "", fmt.Errorf("theme: missing theme id")
	}
	switch strings.ToLower(selection) {
	case "light", "dark":
		return bundledThemeIDForMode(selection)
	case "toggle":
		mode := "light"
		if m.currentThemeMode() == "light" {
			mode = "dark"
		}
		return bundledThemeIDForMode(mode)
	default:
		return selection, nil
	}
}

func (m *Model) currentThemeMode() string {
	current := m.currentThemeID()
	for _, entry := range theme.Catalog() {
		if entry.ID == current {
			return entry.Mode
		}
	}
	return ""
}

func bundledThemeIDForMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	for _, entry := range theme.Catalog() {
		if strings.ToLower(entry.Mode) == mode {
			return entry.ID, nil
		}
	}
	return "", fmt.Errorf("theme: no bundled %s theme", mode)
}

func (m *Model) applyNamedTheme(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("theme: missing theme id")
	}
	th, err := theme.Named(id)
	if err != nil {
		return err
	}

	old := m.currentThemeID()
	if old == "" {
		old = "custom"
	}
	persistErr := m.persistNamedTheme(id)

	theme.Apply(th)
	m.theme = th
	if m.renderer != nil {
		m.renderer.SetTheme(th)
	}
	if m.input != nil {
		m.input.ApplyTheme()
	}
	for i := range m.blocks {
		m.invalidateBlockCache(i)
	}

	body := fmt.Sprintf("theme: %s -> %s", old, th.Name)
	if persistErr != nil {
		body += "\nnot persisted: " + persistErr.Error()
	}
	m.appendBlock(block{kind: "system", body: body})
	m.renderBlocks()
	m.layout()
	return nil
}

func (m *Model) persistNamedTheme(id string) error {
	if m == nil || m.cfg == nil || strings.TrimSpace(m.cfg.ConfigPath) == "" {
		return nil
	}
	data, ok := theme.BuiltinTOML(id)
	if !ok {
		return nil
	}
	path := filepath.Join(filepath.Dir(m.cfg.ConfigPath), "theme.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create theme dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
