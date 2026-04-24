package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/tui/modelpicker"
)

const modelRecentsFile = "model-recents.json"
const modelRecentsLimit = 8

type modelRecentRecord struct {
	ID           string `json:"id"`
	ProviderName string `json:"provider_name"`
	Origin       string `json:"origin"`
}

func (m *Model) modelRecents() []modelpicker.Item {
	path, ok := m.modelRecentsPath()
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var records []modelRecentRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil
	}
	out := make([]modelpicker.Item, 0, len(records))
	for _, r := range records {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		origin := r.Origin
		if origin == "" {
			origin = r.ProviderName
		}
		out = append(out, modelpicker.Item{
			ID:           r.ID,
			Origin:       origin,
			ProviderName: r.ProviderName,
			Recent:       true,
		})
	}
	return out
}

func (m *Model) rememberModelSelection(item modelpicker.Item) {
	if strings.TrimSpace(item.ID) == "" {
		return
	}
	path, ok := m.modelRecentsPath()
	if !ok {
		return
	}
	provider := item.ProviderName
	if provider == "" {
		provider = m.providerName
	}
	origin := item.Origin
	if origin == "" {
		origin = provider
	}
	next := []modelRecentRecord{{
		ID:           item.ID,
		ProviderName: provider,
		Origin:       origin,
	}}
	for _, it := range m.modelRecents() {
		if it.ID == item.ID && it.ProviderName == provider {
			continue
		}
		next = append(next, modelRecentRecord{
			ID:           it.ID,
			ProviderName: it.ProviderName,
			Origin:       it.Origin,
		})
		if len(next) >= modelRecentsLimit {
			break
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0o600)
}

func (m *Model) modelRecentsPath() (string, bool) {
	if m.cfg == nil {
		return "", false
	}
	return filepath.Join(m.cfg.StateDir(), modelRecentsFile), true
}

func prependModelRecents(items, recents []modelpicker.Item) []modelpicker.Item {
	if len(recents) == 0 {
		return items
	}
	out := make([]modelpicker.Item, 0, len(recents)+len(items))
	seen := map[string]struct{}{}
	add := func(it modelpicker.Item) {
		if strings.TrimSpace(it.ID) == "" {
			return
		}
		key := it.ProviderName + "\x00" + it.ID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, it)
	}
	for _, it := range recents {
		it.Recent = true
		add(it)
	}
	for _, it := range items {
		add(it)
	}
	return out
}
