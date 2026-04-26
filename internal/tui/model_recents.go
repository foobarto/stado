package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui/modelpicker"
	"github.com/foobarto/stado/internal/workdirpath"
)

const modelRecentsFile = "model-recents.json"
const modelFavoritesFile = "model-favorites.json"
const modelRecentsLimit = 8
const maxModelStateFileBytes int64 = 256 << 10

type modelRecentRecord struct {
	ID           string `json:"id"`
	ProviderName string `json:"provider_name"`
	Origin       string `json:"origin"`
}

func (m *Model) modelRecents() []modelpicker.Item {
	path, ok := m.modelStatePath(modelRecentsFile)
	if !ok {
		return nil
	}
	return readModelStateItems(path, true, false)
}

func (m *Model) modelFavorites() []modelpicker.Item {
	path, ok := m.modelStatePath(modelFavoritesFile)
	if !ok {
		return nil
	}
	return readModelStateItems(path, false, true)
}

func readModelStateItems(path string, recent, favorite bool) []modelpicker.Item {
	data, err := readModelStateFile(path)
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
			Recent:       recent,
			Favorite:     favorite,
		})
	}
	return out
}

func (m *Model) rememberModelSelection(item modelpicker.Item) {
	if strings.TrimSpace(item.ID) == "" {
		return
	}
	path, ok := m.modelStatePath(modelRecentsFile)
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
	_ = writeModelStateRecords(path, next)
}

func (m *Model) persistDefaultModel(provider, model string) error {
	if m.cfg == nil || strings.TrimSpace(m.cfg.ConfigPath) == "" {
		return nil
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if err := config.WriteDefaults(m.cfg.ConfigPath, provider, model); err != nil {
		return fmt.Errorf("save default model: %w", err)
	}
	m.cfg.Defaults.Model = model
	if provider != "" {
		m.cfg.Defaults.Provider = provider
	}
	return nil
}

func (m *Model) toggleModelFavorite(item modelpicker.Item) bool {
	if strings.TrimSpace(item.ID) == "" {
		return false
	}
	path, ok := m.modelStatePath(modelFavoritesFile)
	if !ok {
		return false
	}
	provider := item.ProviderName
	if provider == "" {
		provider = m.providerName
	}
	origin := item.Origin
	if origin == "" {
		origin = provider
	}
	favorites := m.modelFavorites()
	nextFavorite := true
	for _, it := range favorites {
		if it.ID == item.ID && it.ProviderName == provider {
			nextFavorite = false
			break
		}
	}
	records := []modelRecentRecord{}
	if nextFavorite {
		records = append(records, modelRecentRecord{
			ID:           item.ID,
			ProviderName: provider,
			Origin:       origin,
		})
	}
	for _, it := range favorites {
		if it.ID == item.ID && it.ProviderName == provider {
			continue
		}
		records = append(records, modelRecentRecord{
			ID:           it.ID,
			ProviderName: it.ProviderName,
			Origin:       it.Origin,
		})
	}
	if !writeModelStateRecords(path, records) {
		return false
	}
	return nextFavorite
}

func readModelStateFile(path string) ([]byte, error) {
	root, name, err := modelStateRoot(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("model state file is a symlink: %s", name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("model state file is not regular: %s", name)
	}
	return workdirpath.ReadRootRegularFileLimited(root, name, maxModelStateFileBytes)
}

func writeModelStateRecords(path string, records []modelRecentRecord) bool {
	if err := workdirpath.MkdirAllNoSymlink(filepath.Dir(path), 0o700); err != nil {
		return false
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return false
	}
	root, name, err := modelStateRoot(path)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	return workdirpath.WriteRootFileAtomic(root, name, append(data, '\n'), 0o600) == nil
}

func modelStateRoot(path string) (*os.Root, string, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", fmt.Errorf("invalid model state path: %s", path)
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

func (m *Model) modelStatePath(name string) (string, bool) {
	if m.cfg == nil {
		return "", false
	}
	return filepath.Join(m.cfg.StateDir(), name), true
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

func prependModelFavorites(items, favorites []modelpicker.Item) []modelpicker.Item {
	if len(favorites) == 0 {
		return items
	}
	out := make([]modelpicker.Item, 0, len(favorites)+len(items))
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
	for _, it := range favorites {
		it.Favorite = true
		add(it)
	}
	for _, it := range items {
		add(it)
	}
	return out
}
