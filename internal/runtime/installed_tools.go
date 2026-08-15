package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime/pluginrun"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

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
	manifest  plugins.Manifest
	identity  plugins.RuntimeIdentity
	def       plugins.ToolDef
	schema    map[string]any
	class     tool.Class
	wasmPath  string // <install-dir>/plugin.wasm
	signature string

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
	cfg        *config.Config
	invokeReg  *tools.Registry
	invokeExec *tools.Executor
}

func newInstalledPluginTool(mf plugins.Manifest, identity plugins.RuntimeIdentity, def plugins.ToolDef, wasmPath, signature string, class tool.Class, cfg *config.Config, invokeReg *tools.Registry) tool.Tool {
	var schema map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &schema)
	}
	return &installedPluginTool{
		manifest:  mf,
		identity:  identity,
		def:       def,
		schema:    schema,
		class:     class,
		wasmPath:  wasmPath,
		signature: signature,
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

func (t *installedPluginTool) ToolMetadata() ToolMetadata {
	return ToolMetadata{
		Canonical: CanonicalToolName(t.def.Name), Plugin: t.manifest.Name,
		PackageNamespace: t.identity.Namespace,
		Categories:       append([]string(nil), t.def.Categories...),
		ExtraCategories:  append([]string(nil), t.def.ExtraCategories...),
	}
}

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
	if t.cfg == nil {
		return tool.Result{Error: "installed plugin tool: no cfg bound"}, fmt.Errorf("installed %s: no cfg bound to runtime", t.manifest.Name)
	}
	pluginDir := filepath.Dir(t.wasmPath)
	if err := VerifyInstalledPlugin(ctx, t.cfg, pluginDir, &t.manifest, t.signature); err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("installed %s: verify current admission: %w", t.manifest.Name, err)
	}
	wasmBytes, err := plugins.ReadVerifiedWASM(t.manifest.WASMSHA256, t.wasmPath)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("installed %s: verify: %w", t.manifest.Name, err)
	}
	return pluginrun.Run(ctx, pluginrun.RunArgs{
		Manifest:  t.manifest,
		Identity:  t.identity,
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
		InvokeRegistry:   t.invokeReg,
		InvokeExecutor:   t.invokeExec,
		RegistryCatalog:  NewRegistryCatalogAccess(t.invokeReg, t.identity.Namespace),
		ContextResources: contextResourcesFromSkillContext(ctx),
	}, h)
}

var (
	ignoredProjectPluginWarnings sync.Map
	installedPluginDiagnostics   sync.Map
)

// registerInstalledPluginTools enumerates installed plugins under
// cfg.StateDir()/plugins/, picks the active version per plugin
// (pickActiveVersion), verifies the manifest signature against the
// trust store + wasm sha256, and registers each declared tool as
// an installedPluginTool wrapper with the verified wasm path.
//
// Plugins failing source receipt, signature, host-version, or sha verification
// emit a warning and are skipped. Cross-source installed tool-name collisions
// disable every participating package during complete-surface preflight.
// Replacement of an already-registered built-in remains separately visible
// historical policy until operator-owned exact-source overrides replace it.
//
// Q1/Q2/Q3/Q4 of the design.
func registerInstalledPluginTools(reg *tools.Registry, cfg *config.Config) {
	registerInstalledPluginToolsForSurface(reg, cfg, nil, "")
}

func registerInstalledPluginToolsForSurface(reg *tools.Registry, cfg *config.Config, exactAgentChildTools map[string]bool, childToolOwner string) {
	if cfg == nil {
		return
	}
	// EP-0035: search the project-local .stado/plugins/ dir in addition to the
	// global state dir. AllPluginDirs returns [project, global] in priority
	// order. Collect in that order and skip any canonical source namespace already
	// claimed by a higher-priority dir, so a project copy cleanly shadows the
	// global copy of the same source (rather than merging the two). seen is
	// shared across dirs. Verified against the same (global) trust store.
	seen := map[string]bool{}
	var admitted []admittedInstalledPackage
	projectDir := cfg.ProjectPluginsDir()
	for _, pluginsDir := range cfg.AllPluginDirs() {
		// Project-local plugin autoload is opt-in (Codex #4/#45): an untrusted
		// repo's .stado/plugins/ must not auto-register tools into the live
		// agent (shadowing built-ins, granting CWD-wide fs caps) on a bare
		// `cd`, even when signed by a globally-trusted key. Skip it unless the
		// operator set [plugins] allow_project_plugins.
		if pluginsDir == projectDir && projectDir != "" && !cfg.Plugins.AllowProjectPlugins {
			if shouldWarnIgnoredProjectPlugins(projectDir) {
				emitRegistryDiagnostic("stado: ignoring project-local plugins in %s — set [plugins] allow_project_plugins = true (in user/global config) to autoload them (security).\n", projectDir)
			}
			continue
		}
		admitted = append(admitted, collectPluginsFromDir(cfg, pluginsDir, seen)...)
	}
	registerAdmittedInstalledToolsForSurface(reg, cfg, admitted, exactAgentChildTools, childToolOwner)
}

func shouldWarnIgnoredProjectPlugins(projectDir string) bool {
	_, alreadyWarned := ignoredProjectPluginWarnings.LoadOrStore(projectDir, struct{}{})
	return !alreadyWarned
}

// emitInstalledPluginDiagnosticOnce keeps registry rebuilds from writing raw
// stderr into Bubble Tea's alternate screen. The initial registry build still
// reports each distinct problem before the TUI starts.
func emitInstalledPluginDiagnosticOnce(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if _, alreadyEmitted := installedPluginDiagnostics.LoadOrStore(message, struct{}{}); alreadyEmitted {
		return
	}
	emitRegistryDiagnostic("%s", message)
}

type admittedInstalledPackage struct {
	packageInfo plugins.InstalledPackage
	identity    plugins.RuntimeIdentity
	wasmPath    string
}

// collectPluginsFromDir authenticates active source-keyed packages without
// changing the registry. Registration is deliberately a second phase so the
// complete installed tool surface can be collision-checked before any package
// acquires a model-visible name.
func collectPluginsFromDir(cfg *config.Config, pluginsDir string, seen map[string]bool) []admittedInstalledPackage {
	packages, err := plugins.ListInstalledPackages(pluginsDir)
	if err != nil {
		emitInstalledPluginDiagnosticOnce("stado: warn: enumerate installed plugins in %s: %v\n", pluginsDir, err)
		return nil
	}
	groups := make(map[string][]plugins.InstalledPackage)
	for _, pkg := range packages {
		groups[pkg.Identity.Namespace] = append(groups[pkg.Identity.Namespace], pkg)
	}
	var namespaces []string
	for namespace := range groups {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	var admitted []admittedInstalledPackage
	for _, namespace := range namespaces {
		candidates := groups[namespace]
		if seen[namespace] {
			continue // a higher-priority dir already provided this plugin
		}
		// Claim the canonical source namespace so a lower-priority copy is skipped
		// entirely — even if this copy fails to verify below, a project
		// plugin authoritatively shadows the global one (no silent fallback).
		seen[namespace] = true
		selected, ok, selectErr := plugins.PickActivePackage(pluginsDir, namespace, candidates)
		if selectErr != nil {
			emitInstalledPluginDiagnosticOnce("stado: warn: select installed source %s: %v\n", namespace, selectErr)
			continue
		}
		if !ok {
			continue
		}
		dir, mf, sig := selected.Dir, &selected.Manifest, selected.Signature
		if err := VerifyInstalledPlugin(context.Background(), cfg, dir, mf, sig); err != nil {
			emitInstalledPluginDiagnosticOnce("stado: warn: plugin %s signature failed: %v; not registered\n", selected.Identity.Canonical, err)
			continue
		}
		identity := selected.Identity
		if identity != selected.Identity {
			emitInstalledPluginDiagnosticOnce("stado: warn: plugin %s identity changed during trust verification; not registered\n", selected.Identity.Canonical)
			continue
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		// Re-verify wasm sha now to catch tampering between install
		// and registration.
		if _, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath); err != nil {
			emitInstalledPluginDiagnosticOnce("stado: warn: plugin %s wasm verify: %v; not registered\n", selected.Identity.Canonical, err)
			continue
		}
		admitted = append(admitted, admittedInstalledPackage{packageInfo: selected, identity: identity, wasmPath: wasmPath})
	}
	return admitted
}

// registerAdmittedInstalledTools rejects every package participating in a
// cross-source tool-name collision. Neither filesystem/root order nor Go map
// iteration may choose which signed implementation owns a model-visible name.
// A collision with any already-registered bundled/native/external owner also
// rejects the entire installed package before registration starts. The only
// replacement path is the operator-owned exact [tools].overrides contract.
func registerAdmittedInstalledTools(reg *tools.Registry, cfg *config.Config, admitted []admittedInstalledPackage) {
	registerAdmittedInstalledToolsForSurface(reg, cfg, admitted, nil, "")
}

func registerAdmittedInstalledToolsForSurface(reg *tools.Registry, cfg *config.Config, admitted []admittedInstalledPackage, exactAgentChildTools map[string]bool, childToolOwner string) {
	owners := make(map[string][]int)
	for i := range admitted {
		for _, def := range admitted[i].packageInfo.Manifest.Tools {
			owners[def.Name] = append(owners[def.Name], i)
		}
	}
	rejected := make(map[int]bool)
	var names []string
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		indices := owners[name]
		if len(indices) < 2 {
			continue
		}
		var sources []string
		for _, index := range indices {
			rejected[index] = true
			sources = append(sources, admitted[index].identity.Canonical)
		}
		sort.Strings(sources)
		emitInstalledPluginDiagnosticOnce("stado: warn: installed tool %s is declared by multiple authenticated sources (%v); all colliding packages are disabled\n", name, sources)
	}
	// Preflight against the registry as it existed before installed package
	// admission. Reject package-atomically: registering its non-colliding tools
	// before discovering a later collision would expose a partial signed
	// application contract and make manifest order authoritative.
	for i := range admitted {
		if rejected[i] {
			continue
		}
		var collisions []string
		for _, def := range admitted[i].packageInfo.Manifest.Tools {
			if _, exists := reg.Get(def.Name); exists {
				collisions = append(collisions, def.Name)
			}
		}
		if len(collisions) == 0 {
			continue
		}
		sort.Strings(collisions)
		rejected[i] = true
		emitInstalledPluginDiagnosticOnce("stado: warn: installed source %s collides with existing registry owner(s) for %v; the entire package is disabled (use an explicit [tools].overrides selector to replace a tool)\n", admitted[i].identity.Canonical, collisions)
	}
	for i := range admitted {
		if rejected[i] {
			continue
		}
		selected := admitted[i].packageInfo
		mf := &selected.Manifest
		identity := admitted[i].identity
		wasmPath := admitted[i].wasmPath
		for _, def := range mf.Tools {
			if def.AgentChildOnly && (!exactAgentChildTools[def.Name] || identity.Namespace != childToolOwner) {
				continue
			}
			capabilities, capErr := mf.EffectiveToolCapabilities(def)
			if capErr != nil {
				emitInstalledPluginDiagnosticOnce("stado: warn: plugin %s tool %s: capabilities: %v; package admission invariant violated\n",
					selected.Identity.Canonical, def.Name, capErr)
				continue
			}
			class, err := pluginRuntime.EffectiveToolClass(def, capabilities)
			if err != nil {
				emitInstalledPluginDiagnosticOnce("stado: warn: plugin %s tool %s: class resolve: %v; defaulting to exec\n",
					selected.Identity.Canonical, def.Name, err)
			}
			reg.Register(newInstalledPluginTool(*mf, identity, def, wasmPath, selected.Signature, class, cfg, reg))
		}
	}
}

// InstalledModuleForTool returns the authenticated module contract carried by
// one exact registry-selected installed adapter. Object selection is the
// authority boundary: resolving by a process-global name would let concurrent
// registry builds cross-wire same-named packages from different config roots.
func InstalledModuleForTool(candidate tool.Tool) (plugins.Manifest, plugins.RuntimeIdentity, string, bool) {
	installed, ok := candidate.(*installedPluginTool)
	if !ok {
		return plugins.Manifest{}, plugins.RuntimeIdentity{}, "", false
	}
	return installed.manifest, installed.identity, installed.wasmPath, true
}

// RuntimeIdentityForPluginDir resolves an installed plugin's signed EP-39 lock
// identity, or an explicit source-bound local-development identity when the
// directory was installed without a remote lock entry.
func RuntimeIdentityForPluginDir(pluginDir string, manifest plugins.Manifest) (plugins.RuntimeIdentity, error) {
	return plugins.RuntimeIdentityForInstalledDir(pluginDir, manifest)
}
