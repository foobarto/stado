package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	installReceiptSchema = 1
	installReceiptFile   = "install-receipts.json"
)

// installReceipt is the user-state admission receipt for one exact store row.
// Project repositories may contain .stado/plugins and plugin-lock.toml, so
// neither of those files proves that the host accepted an installation. The
// receipt lives outside project control under the user's stado state. StoreKey
// is sufficient payload because InstallRecord.Validate recomputes it over the
// complete record (source, namespace, revisions, manifest/WASM digests, signer,
// and version) before this receipt is consulted.
type installReceipt struct {
	Schema      int    `json:"schema"`
	PluginsRoot string `json:"plugins_root"`
	StoreKey    string `json:"store_key"`
}

func receiptPath(stateDir string) string {
	return filepath.Join(stateDir, "plugins", installReceiptFile)
}

func canonicalPluginsRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func loadInstallReceipts(path string) ([]installReceipt, error) {
	data, err := readPluginStateFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts []installReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipts); err != nil {
		return nil, fmt.Errorf("parse installed-plugin receipts: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("parse installed-plugin receipts: trailing JSON value")
	}
	seen := make(map[string]struct{}, len(receipts))
	for _, receipt := range receipts {
		if receipt.Schema != installReceiptSchema || !validInstalledStoreKey(receipt.StoreKey) || receipt.PluginsRoot == "" {
			return nil, errors.New("invalid installed-plugin receipt")
		}
		key := receipt.PluginsRoot + "\x00" + receipt.StoreKey
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("duplicate installed-plugin receipt")
		}
		seen[key] = struct{}{}
	}
	return receipts, nil
}

func saveInstallReceipts(path string, receipts []installReceipt) error {
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].PluginsRoot == receipts[j].PluginsRoot {
			return receipts[i].StoreKey < receipts[j].StoreKey
		}
		return receipts[i].PluginsRoot < receipts[j].PluginsRoot
	})
	data, err := json.MarshalIndent(receipts, "", "  ")
	if err != nil {
		return err
	}
	return writePluginStateFileAtomic(path, append(data, '\n'), 0o600)
}

func WriteInstallReceipt(stateDir, pluginsRoot string, record InstallRecord) error {
	if !validInstalledStoreKey(record.StoreKey) {
		return errors.New("installed-plugin receipt requires a valid store key")
	}
	canonicalRoot, err := canonicalPluginsRoot(pluginsRoot)
	if err != nil {
		return err
	}
	path := receiptPath(stateDir)
	return withPluginFileLock(path, func() error {
		receipts, err := loadInstallReceipts(path)
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			if receipt.PluginsRoot == canonicalRoot && receipt.StoreKey == record.StoreKey {
				return nil
			}
		}
		receipts = append(receipts, installReceipt{Schema: installReceiptSchema, PluginsRoot: canonicalRoot, StoreKey: record.StoreKey})
		return saveInstallReceipts(path, receipts)
	})
}

func CheckInstallReceipt(stateDir, pluginsRoot string, record InstallRecord) error {
	canonicalRoot, err := canonicalPluginsRoot(pluginsRoot)
	if err != nil {
		return err
	}
	receipts, err := loadInstallReceipts(receiptPath(stateDir))
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.PluginsRoot == canonicalRoot && receipt.StoreKey == record.StoreKey {
			return nil
		}
	}
	return errors.New("installed plugin has no host-owned exact install receipt; reinstall it explicitly")
}

func RemoveInstallReceipts(stateDir string, removals map[string]map[string]struct{}) error {
	path := receiptPath(stateDir)
	return withPluginFileLock(path, func() error {
		receipts, err := loadInstallReceipts(path)
		if err != nil {
			return err
		}
		kept := receipts[:0]
		for _, receipt := range receipts {
			keys := removals[receipt.PluginsRoot]
			if _, remove := keys[receipt.StoreKey]; remove {
				continue
			}
			kept = append(kept, receipt)
		}
		return saveInstallReceipts(path, kept)
	})
}

func RemoveInstallReceipt(stateDir, pluginsRoot, storeKey string) error {
	canonicalRoot, err := canonicalPluginsRoot(pluginsRoot)
	if err != nil {
		return err
	}
	return RemoveInstallReceipts(stateDir, map[string]map[string]struct{}{
		canonicalRoot: {storeKey: {}},
	})
}
