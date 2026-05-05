package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/pkg/tool"
)

// activeVersionMarker reads the per-plugin active-version marker
// written by `stado plugin use <name>@<version>` (cmd/stado/
// plugin_use_dev.go:48). Returns the trimmed version string when
// present; "" when the marker is missing or unreadable.
func activeVersionMarker(stateDir, pluginName string) string {
	markerPath := filepath.Join(stateDir, "plugins", "active", pluginName)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// pickActiveVersion returns which version of pluginName to register,
// given the list of candidates found on disk. Pin precedence:
//  1. <stateDir>/plugins/active/<name> marker file (set by
//     `stado plugin use <name>@<version>`); only honoured when the
//     marker's version is among candidates. Marker pointing at a
//     version not on disk returns "" (caller logs + skips).
//  2. Highest semver among candidates.
//
// Returns "" if (1) misses and candidates is empty.
func pickActiveVersion(stateDir, pluginName string, candidates []string) string {
	if marker := activeVersionMarker(stateDir, pluginName); marker != "" {
		for _, v := range candidates {
			if v == marker {
				return marker
			}
		}
		return ""
	}
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, v := range candidates[1:] {
		if semver.Compare(v, best) > 0 {
			best = v
		}
	}
	return best
}

// installedPluginTool wraps an installed plugin's declared tool as
// a wasm-backed registry entry. wasm bytes are loaded lazily on
// first invocation (the registry build path runs on every CLI
// invocation; eager-loading every plugin's wasm at registration
// would be expensive for operators with many installed plugins).
//
// The manifest carries the verified WASMSHA256; ReadVerifiedWASM
// re-checks the sha at load time, so a tampered plugin.wasm fails
// at invoke rather than silently succeeding.
//
// Run() returns a sentinel Result.Error since installed-plugin
// invocation goes through cmd/stado/tool_run.go's shared helper
// (runPluginInvocation), not this wrapper directly. The wrapper
// exists so Registry.All() / tool list / tools.search reflect
// installed plugins as first-class entries.
type installedPluginTool struct {
	manifest plugins.Manifest
	def      plugins.ToolDef
	schema   map[string]any
	class    tool.Class
	wasmPath string // <install-dir>/plugin.wasm
}

func newInstalledPluginTool(mf plugins.Manifest, def plugins.ToolDef, wasmPath string, class tool.Class) tool.Tool {
	var schema map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &schema)
	}
	return &installedPluginTool{
		manifest: mf,
		def:      def,
		schema:   schema,
		class:    class,
		wasmPath: wasmPath,
	}
}

func (t *installedPluginTool) Name() string        { return t.def.Name }
func (t *installedPluginTool) Description() string { return t.def.Description }
func (t *installedPluginTool) Schema() map[string]any {
	if t.schema == nil {
		return map[string]any{"type": "object"}
	}
	return t.schema
}
func (t *installedPluginTool) Class() tool.Class { return t.class }

// Run is a sentinel — installed-plugin invocation goes through
// runPluginInvocation in cmd/stado/plugin_invoke_shared.go via
// tool_run.go's installed branch.
func (t *installedPluginTool) Run(_ context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	return tool.Result{
		Error: "installed plugin tool not invokable directly via Tool.Run; route through stado tool run <name>",
	}, nil
}

// groupInstalledByName scans pluginsDir for "<name>-<version>"
// subdirectories and returns a map of name → versions. Entries that
// don't match the expected pattern (no -v prefix, the "active"
// metadata subdir, plain files) are skipped. A missing pluginsDir
// is not an error — returns an empty map.
func groupInstalledByName(pluginsDir string) (map[string][]string, error) {
	out := map[string][]string{}
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "active" {
			continue
		}
		name, version, ok := splitInstalledID(e.Name())
		if !ok {
			continue
		}
		out[name] = append(out[name], version)
	}
	return out, nil
}

// splitInstalledID splits "<name>-<version>" into name + version.
// Splits on the last "-v<digit>" boundary so multi-dash names like
// "htb-lab" or "exfil-server" round-trip correctly. Returns ok=false
// when the suffix isn't a "v<digit>..." shape.
func splitInstalledID(id string) (name, version string, ok bool) {
	for i := len(id) - 1; i >= 1; i-- {
		if id[i] == '-' && i+1 < len(id) && id[i+1] == 'v' && i+2 < len(id) && id[i+2] >= '0' && id[i+2] <= '9' {
			return id[:i], id[i+1:], true
		}
	}
	return "", "", false
}
