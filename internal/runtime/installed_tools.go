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
	"github.com/foobarto/stado/internal/runtime/pluginrun"
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

	// Codex C4/Q P2 — pre-fix the cfg + invokeReg were package globals
	// rebound by every registerInstalledPluginTools call. When a
	// caller did `/tool info` (which builds an UNFILTERED registry to
	// list every tool the operator could enable), the globals got
	// rebound to that unfiltered registry; the next stado_tool_invoke
	// from an installed plugin then dispatched against an unfiltered
	// surface, silently expanding the plugin's tool surface past what
	// [tools].enabled / .disabled scoped. Storing them per-tool ties
	// each instance to the registry-build that created it; subsequent
	// builds get their own instances with their own pointers, so
	// nested invokes can't leak across builds.
	cfg       *config.Config
	invokeReg *tools.Registry
}

func newInstalledPluginTool(mf plugins.Manifest, def plugins.ToolDef, wasmPath string, class tool.Class, cfg *config.Config, invokeReg *tools.Registry) tool.Tool {
	var schema map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &schema)
	}
	return &installedPluginTool{
		manifest:  mf,
		def:       def,
		schema:    schema,
		class:     class,
		wasmPath:  wasmPath,
		cfg:       cfg,
		invokeReg: invokeReg,
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

// PluginName returns the installed plugin's manifest name (e.g.
// "gtfobins"). Used by the TUI landing render to group autoloaded
// tools by source plugin.
func (t *installedPluginTool) PluginName() string { return t.manifest.Name }

// Run dispatches the installed plugin via pluginrun.Run. Re-reads the
// verified wasm from disk per call (no in-memory cache); the up-front
// signature + sha verification at registration time ensures the bytes
// haven't been swapped, but the on-disk file may have been touched
// between then and now — re-verify defensively.
//
// Pre-Step-0 this returned a sentinel error and only the CLI's
// `stado tool run` dispatcher special-cased installed plugins. The
// agent loop and MCP server's executor.Run (which calls Tool.Run
// directly) hit the sentinel and silently failed for installed
// plugins — fixed here so all dispatch paths are uniform.
func (t *installedPluginTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	wasmBytes, err := plugins.ReadVerifiedWASM(t.manifest.WASMSHA256, t.wasmPath)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("installed %s: verify: %w", t.manifest.Name, err)
	}
	if t.cfg == nil {
		return tool.Result{Error: "installed plugin tool: no cfg bound"}, fmt.Errorf("installed %s: no cfg bound to runtime", t.manifest.Name)
	}
	return pluginrun.Run(ctx, pluginrun.RunArgs{
		Manifest:  t.manifest,
		WasmBytes: wasmBytes,
		ToolName:  t.def.Name,
		Args:      args,
		Cfg:       t.cfg,
		Workdir:   h.Workdir(),
		// SessionID intentionally empty: the agent-loop tool.Host
		// already carries any session bridge through the lifecycle
		// wiring pluginrun's attachLifecycleBridges pulls off h.
		// CLI invocations route through plugin_invoke_shared.go which
		// builds a SessionBridgeBuilder from --session.
		InvokeRegistry: t.invokeReg,
	}, h)
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
	ts := plugins.NewTrustStore(stateDir)

	// Codex C4/Q P2: per-tool cfg + reg storage (see installedPluginTool
	// struct comment). Reset package-level lookup state once for this build.
	installedRegistryMu.Lock()
	installedByTool = map[string]installedRecord{}
	installedRegistryMu.Unlock()

	// EP-0035: search the project-local .stado/plugins/ dir in addition to the
	// global state dir. AllPluginDirs returns [project, global] in priority
	// order. Register in that order and skip any plugin NAME already claimed by
	// a higher-priority dir, so a project plugin cleanly SHADOWS the global one
	// of the same name (rather than per-tool merging the two copies). seen is
	// shared across dirs. Verified against the same (global) trust store.
	seen := map[string]bool{}
	projectDir := cfg.ProjectPluginsDir()
	for _, pluginsDir := range cfg.AllPluginDirs() {
		// Project-local plugin autoload is opt-in (Codex #4/#45): an untrusted
		// repo's .stado/plugins/ must not auto-register tools into the live
		// agent (shadowing built-ins, granting CWD-wide fs caps) on a bare
		// `cd`, even when signed by a globally-trusted key. Skip it unless the
		// operator set [plugins] allow_project_plugins.
		if pluginsDir == projectDir && projectDir != "" && !cfg.Plugins.AllowProjectPlugins {
			fmt.Fprintf(os.Stderr, "stado: ignoring project-local plugins in %s — set [plugins] allow_project_plugins = true (in user/global config) to autoload them (security).\n", projectDir)
			continue
		}
		registerPluginsFromDir(reg, cfg, ts, stateDir, pluginsDir, seen)
	}
}

// registerPluginsFromDir registers every active installed-plugin tool found
// under pluginsDir into reg, skipping any plugin name already in seen (claimed
// by a higher-priority dir) and marking the names it claims. stateDir is the
// global state dir used for active-version pins (a project plugin with no
// global pin falls back to its highest version). A missing pluginsDir is a
// no-op.
func registerPluginsFromDir(reg *tools.Registry, cfg *config.Config, ts *plugins.TrustStore, stateDir, pluginsDir string, seen map[string]bool) {
	groups, err := groupInstalledByName(pluginsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado: warn: enumerate installed plugins in %s: %v\n", pluginsDir, err)
		return
	}
	for name, versions := range groups {
		if seen[name] {
			continue // a higher-priority dir already provided this plugin
		}
		// Claim the name for this dir so a lower-priority copy is skipped
		// entirely — even if this copy fails to verify below, a project
		// plugin authoritatively shadows the global one (no silent fallback).
		seen[name] = true
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
				fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s tool %s: class resolve: %v; defaulting to exec\n",
					name, version, def.Name, err)
			}
			reg.Register(newInstalledPluginTool(*mf, def, wasmPath, class, cfg, reg))

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
// bundled.LookupModuleByToolName. Used by cmd/stado/tool_run.go
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

// ResolveInstalledPluginDir takes an operator-friendly bare plugin name
// (e.g. "gtfobins") and returns the on-disk directory of its active
// version (e.g. "<state>/plugins/gtfobins-0.1.0"). Resolution mirrors
// registerInstalledPluginTools: groupInstalledByName + pickActiveVersion.
//
// Returns ok=false when the plugin isn't installed, or when an active-
// version marker points at a version that's not on disk. No signature
// verification — callers (e.g. plugin info) read the manifest after.
func ResolveInstalledPluginDir(cfg *config.Config, name string) (string, bool) {
	if cfg == nil || name == "" {
		return "", false
	}
	stateDir := cfg.StateDir()
	// EP-0035: check project-local .stado/plugins/ before the global state dir
	// (AllPluginDirs returns [project, global]) so a project plugin wins,
	// matching registerInstalledPluginTools' precedence.
	for _, pluginsDir := range cfg.AllPluginDirs() {
		groups, err := groupInstalledByName(pluginsDir)
		if err != nil {
			continue
		}
		versions, ok := groups[name]
		if !ok {
			continue
		}
		version := pickActiveVersion(stateDir, name, versions)
		if version == "" {
			continue
		}
		return filepath.Join(pluginsDir, name+"-"+version), true
	}
	return "", false
}
