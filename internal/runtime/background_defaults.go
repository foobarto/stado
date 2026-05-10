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
// This file owns *which of those the host turns on, and with what
// declared manifest*. Keeping host-side policy out of bundled lets that
// package stay an asset store rather than a policy store.
//
// TODO: dedupe autoCompactManifest() with
// plugins/bundled/auto-compact/plugin.manifest.template.json — see
// follow-up spec kill-autocompact-manifest-duplication. Today the
// canonical manifest is duplicated in two source-of-truth locations
// and they can drift.

package runtime

import (
	"encoding/json"

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
		Manifest: autoCompactManifest(),
		WASM:     bundled.MustWasm(autoCompactID),
	}, true
}

func isAutoCompactID(id string) bool {
	return id == autoCompactID || id == autoCompactID+"-"+version.Version
}

func autoCompactManifest() plugins.Manifest {
	return plugins.Manifest{
		Name:         autoCompactID,
		Version:      version.Version,
		Author:       bundled.Author,
		Capabilities: []string{"session:observe", "session:read", "session:fork", "llm:invoke:30000"},
		Tools: []plugins.ToolDef{{
			Name:        "compact",
			Description: "Summarise the current session and fork into a fresh session seeded with the summary. Skips when token_count is below threshold_tokens unless invoked as hard-threshold recovery.",
			Schema:      autoCompactSchema(),
		}},
		MinStadoVersion: version.Version,
		Nonce:           "bundled-auto-compact",
	}
}

func autoCompactSchema() string {
	raw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"threshold_tokens": map[string]any{
				"type":        "integer",
				"description": "Skip compaction if session token count is below this; default 10000.",
			},
		},
	})
	if err != nil {
		return `{"type":"object"}`
	}
	return string(raw)
}
