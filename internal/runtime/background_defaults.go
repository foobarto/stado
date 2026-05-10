// Package runtime — host-side bootstrap policy for default-on
// background plugins.
//
// This file decides which bundled wasm plugins the host should load by
// default at startup, and how to materialise them (manifest + bytes)
// when an ID is requested. Today auto-compact is the only such plugin;
// future flagged or experimental defaults register here too.
//
// The split with internal/plugins/bundled is deliberate: the bundled
// package owns *what wasm ships in the binary* (assets + inventory).
// This file owns *which of those the host turns on*. Manifest content
// is loaded from the bundled package's embedded manifests/ tree, which
// is sourced from each plugin's plugin.manifest.template.json — single
// canonical source per plugin.

package runtime

import (
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
	"github.com/foobarto/stado/internal/version"
)

const autoCompactID = "auto-compact"

// BundledBackgroundPlugin is a shipped plugin that is embedded in the
// stado binary rather than loaded from the installed-plugin state dir.
type BundledBackgroundPlugin struct {
	ID       string
	Manifest plugins.Manifest
	WASM     []byte
}

func DefaultBackgroundPlugins() []string {
	return []string{autoCompactID}
}

func LookupBackgroundPlugin(id string) (*BundledBackgroundPlugin, bool) {
	if !isAutoCompactID(id) {
		return nil, false
	}
	return &BundledBackgroundPlugin{
		ID:       autoCompactID,
		Manifest: bundled.MustManifest(autoCompactID),
		WASM:     bundled.MustWasm(autoCompactID),
	}, true
}

// isAutoCompactID accepts both the bare id and the version-suffixed
// form ("auto-compact-<stado-version>"). The suffixed form is a
// runtime ID convention (used when matching installed-plugin
// directory names that carry the stado release version); it is not
// tied to the manifest's own version field.
func isAutoCompactID(id string) bool {
	return id == autoCompactID || id == autoCompactID+"-"+version.Version
}
