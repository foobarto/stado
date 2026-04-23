package bundledplugins

import (
	"encoding/json"

	"github.com/foobarto/stado/internal/plugins"
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
		WASM:     MustWasm(autoCompactID),
	}, true
}

func isAutoCompactID(id string) bool {
	return id == autoCompactID || id == autoCompactID+"-"+version.Version
}

func autoCompactManifest() plugins.Manifest {
	return plugins.Manifest{
		Name:         autoCompactID,
		Version:      version.Version,
		Author:       Author,
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
