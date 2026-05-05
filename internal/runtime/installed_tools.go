package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/semver"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tools"
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
		normMarker := semverize(marker)
		for _, v := range candidates {
			if semverize(v) == normMarker {
				return v
			}
		}
		return ""
	}
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, v := range candidates[1:] {
		if semver.Compare(semverize(v), semverize(best)) > 0 {
			best = v
		}
	}
	return best
}

// semverize prepends "v" to a version string when missing, since
// golang.org/x/mod/semver requires the v-prefixed form. Real install
// dirs use the no-v form (e.g. "0.1.0"); this lets us compare them
// without rewriting the on-disk convention.
func semverize(v string) string {
	if len(v) > 0 && v[0] == 'v' {
		return v
	}
	return "v" + v
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
// Accepts both "name-0.1.0" and "name-v0.1.0" forms (real installs
// use the no-v form; the v-prefixed form is what golang.org/x/mod/
// semver expects internally — pickActiveVersion normalizes for that).
// Splits on the last "-" followed by a digit or "v<digit>" so multi-
// dash names like "htb-lab" round-trip correctly. Returns ok=false
// when the suffix isn't a version-shaped string.
func splitInstalledID(id string) (name, version string, ok bool) {
	for i := len(id) - 1; i >= 1; i-- {
		if id[i] != '-' {
			continue
		}
		rest := id[i+1:]
		if len(rest) == 0 {
			continue
		}
		// Accept: digit start (0.1.0), or v + digit (v0.1.0).
		switch {
		case rest[0] >= '0' && rest[0] <= '9':
			return id[:i], rest, true
		case rest[0] == 'v' && len(rest) >= 2 && rest[1] >= '0' && rest[1] <= '9':
			return id[:i], rest, true
		}
	}
	return "", "", false
}

// installedRegistryMu protects the package-level installedByTool
// map populated by registerInstalledPluginTools and consumed by
// LookupInstalledModule (used by cmd/stado/tool_run.go to dispatch).
var (
	installedRegistryMu sync.Mutex
	installedByTool     = map[string]installedRecord{}
)

type installedRecord struct {
	Manifest plugins.Manifest
	WasmPath string
}

// registerInstalledPluginTools enumerates installed plugins under
// cfg.StateDir()/plugins/, picks the active version per plugin
// (pickActiveVersion), verifies the manifest signature against the
// trust store + wasm sha256, and registers each declared tool as
// an installedPluginTool wrapper with the verified wasm path.
//
// Plugins failing signature or sha verification emit a stado: warn
// line on stderr and are skipped. Tool-name collisions with already-
// registered tools (typically bundled) emit a stado: info line at
// registration time and overwrite (Q4 — installed wins, per
// tools.Registry.Register semantics).
//
// Q1/Q2/Q3/Q4 of the design.
func registerInstalledPluginTools(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil {
		return
	}
	stateDir := cfg.StateDir()
	pluginsDir := filepath.Join(stateDir, "plugins")

	groups, err := groupInstalledByName(pluginsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado: warn: enumerate installed plugins: %v\n", err)
		return
	}

	ts := plugins.NewTrustStore(stateDir)

	// Reset package-level lookup state for this build.
	installedRegistryMu.Lock()
	installedByTool = map[string]installedRecord{}
	installedRegistryMu.Unlock()

	for name, versions := range groups {
		version := pickActiveVersion(stateDir, name, versions)
		if version == "" {
			continue
		}
		dir := filepath.Join(pluginsDir, name+"-"+version)
		mf, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s manifest load: %v\n", name, version, err)
			continue
		}
		if err := ts.VerifyManifest(mf, sig); err != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s signature failed: %v; not registered\n", name, version, err)
			continue
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		// Re-verify wasm sha now to catch tampering between install
		// and registration.
		if _, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath); err != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s wasm verify: %v; not registered\n", name, version, err)
			continue
		}
		for _, def := range mf.Tools {
			if _, exists := reg.Get(def.Name); exists {
				fmt.Fprintf(os.Stderr, "stado: info: plugin %s@%s overrides registered tool %s\n", name, version, def.Name)
			}
			class, err := pluginRuntime.EffectiveToolClass(def, mf.Capabilities)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s tool %s: class resolve: %v; defaulting to non-mutating\n",
					name, version, def.Name, err)
				class = tool.ClassNonMutating
			}
			reg.Register(newInstalledPluginTool(*mf, def, wasmPath, class))

			installedRegistryMu.Lock()
			installedByTool[def.Name] = installedRecord{
				Manifest: *mf,
				WasmPath: wasmPath,
			}
			installedRegistryMu.Unlock()
		}
	}
}

// LookupInstalledModule returns the manifest + wasm path for the
// named installed-plugin tool. Symmetric with
// bundledplugins.LookupModuleByToolName. Used by cmd/stado/tool_run.go
// to dispatch installed-plugin invocation through runPluginInvocation.
func LookupInstalledModule(toolName string) (plugins.Manifest, string, bool) {
	installedRegistryMu.Lock()
	defer installedRegistryMu.Unlock()
	rec, ok := installedByTool[toolName]
	if !ok {
		return plugins.Manifest{}, "", false
	}
	return rec.Manifest, rec.WasmPath, true
}
