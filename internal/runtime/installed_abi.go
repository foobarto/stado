package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/tetratelabs/wazero"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// ABIIssue describes a single installed-plugin ABI mismatch found by
// VerifyInstalledPluginsABI. The Plugin / Version pair identifies the
// failing install. CompileError captures decoder failures (truncated
// or malformed wasm); when set, MissingExports / RemovedHostImports
// are unset because the module never decoded.
//
// MissingExports lists the wasm-side exports the runtime expected but
// the plugin doesn't provide (e.g. the plugin is missing
// stado_tool_<name> for a tool declared in the manifest).
//
// RemovedHostImports lists the host-side imports the plugin expects
// from the "stado" namespace but that the runtime no longer provides.
// This is the v0.45.0 / D1 case — plugins compiled against v0.44.x
// import stado_fs_tool_read / stado_fs_tool_write etc., which were
// deleted in EP-no-internal-tools Step 7. Pre-fix these failed
// silently at instantiate time during the first tool call; the eager
// verifier surfaces them at session/new instead.
type ABIIssue struct {
	Plugin             string
	Version            string
	MissingExports     []string
	RemovedHostImports []string
	CompileError       string
}

// Missing is a back-compat alias for the union of MissingExports and
// RemovedHostImports — older callers (tests, log formatters) treated
// missing items as a single bag. New code should branch on the two
// fields separately so the operator-facing message can distinguish
// "rebuild — old plugin uses removed imports" from "rebuild — new
// tool was added to manifest but stado_tool_<name> isn't exported".
func (i ABIIssue) Missing() []string {
	if len(i.MissingExports) == 0 && len(i.RemovedHostImports) == 0 {
		return nil
	}
	out := make([]string, 0, len(i.MissingExports)+len(i.RemovedHostImports))
	out = append(out, i.MissingExports...)
	for _, name := range i.RemovedHostImports {
		out = append(out, "host:"+name)
	}
	sort.Strings(out)
	return out
}

// String formats an ABIIssue for human-readable error reporting.
func (i ABIIssue) String() string {
	if i.CompileError != "" {
		return fmt.Sprintf("%s@%s: wasm compile failed: %s", i.Plugin, i.Version, i.CompileError)
	}
	parts := []string{}
	if len(i.RemovedHostImports) > 0 {
		parts = append(parts, fmt.Sprintf("imports removed in this stado version: %v (rebuild required)", i.RemovedHostImports))
	}
	if len(i.MissingExports) > 0 {
		parts = append(parts, fmt.Sprintf("missing exports: %v", i.MissingExports))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s@%s: ABI mismatch (no detail)", i.Plugin, i.Version)
	}
	out := i.Plugin + "@" + i.Version + ": "
	for j, p := range parts {
		if j > 0 {
			out += "; "
		}
		out += p
	}
	return out
}

// providedHostImports returns the set of stado-namespace function
// names the runtime currently provides. Implemented by spinning up a
// throwaway runtime, registering every host import via
// InstallHostImports against a zero-value Host (caps gate at call time,
// not registration), then enumerating the resulting "stado" module's
// exports. Used by VerifyInstalledPluginsABI to detect plugins that
// import host functions removed in a stado release.
func providedHostImports(ctx context.Context) (map[string]bool, error) {
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify rt: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	manifest := plugins.Manifest{Name: "abi-verifier", Version: "dev"}
	identity, err := plugins.RuntimeIdentityForBundledSource("abi-verifier", manifest)
	if err != nil {
		return nil, fmt.Errorf("verify host identity: %w", err)
	}
	host := pluginRuntime.NewHostWithIdentity(manifest, identity, "", nil)
	if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
		return nil, fmt.Errorf("verify install host imports: %w", err)
	}

	mod := rt.Wazero().Module(pluginRuntime.NamespaceStado)
	if mod == nil {
		return nil, fmt.Errorf("verify: stado namespace module missing post-install")
	}
	out := map[string]bool{}
	for name := range mod.ExportedFunctionDefinitions() {
		out[name] = true
	}
	return out, nil
}

// VerifyInstalledPluginsABI eagerly checks every installed-and-active
// plugin in cfg.StateDir()/plugins/ against the runtime ABI:
//
//  1. wasm-side exports: stado_alloc, stado_free, and one
//     stado_tool_<name> export per ToolDef in the manifest.
//  2. host-side imports: every function the plugin imports from the
//     "stado" namespace must be in the host's currently-provided set.
//     This catches plugins built against an older runtime that
//     reference host functions deleted in this version (e.g. v0.44.x
//     plugins importing stado_fs_tool_read after Step 7 removed it).
//
// Returns the issue set (empty when everything compiles, exports
// cleanly, AND only imports current host functions). Caller decides
// what to do with issues — surface them as a session/new error,
// log + continue, etc.
//
// Cost: one host-import installation (per call, not per plugin —
// reused) + wazero.CompileModule per plugin. CompileModule decodes
// without instantiating; sub-second total for typical install counts.
//
// Skips signature- or sha-failing plugins silently — those are already
// surfaced at registerInstalledPluginTools time as stado: warn lines
// and never reach the registry, so reporting them again here would
// double-warn the operator.
func VerifyInstalledPluginsABI(ctx context.Context, cfg *config.Config) ([]ABIIssue, error) {
	if cfg == nil {
		return nil, nil
	}
	stateDir := cfg.StateDir()
	pluginsDir := filepath.Join(stateDir, "plugins")
	groups, err := groupInstalledByName(pluginsDir)
	if err != nil {
		return nil, fmt.Errorf("enumerate installed plugins: %w", err)
	}
	if len(groups) == 0 {
		return nil, nil
	}

	// Build the host-import set once for the whole verify pass.
	provided, err := providedHostImports(ctx)
	if err != nil {
		// Non-fatal: degrade to export-only checks. The caller already
		// gets actionable info on missing tool exports; missing-host-
		// import detection is a v0.45.0+ enhancement.
		emitRegistryDiagnostic("stado: warn: ABI verify host-import set unavailable: %v\n", err)
		provided = nil
	}

	ts := plugins.NewTrustStore(stateDir)

	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig())
	defer func() { _ = rt.Close(ctx) }()

	var issues []ABIIssue
	for name, versions := range groups {
		version := pickActiveVersion(stateDir, name, versions)
		if version == "" {
			continue
		}
		dir := filepath.Join(pluginsDir, name+"-"+version)
		mf, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			continue
		}
		if err := ts.VerifyManifest(mf, sig); err != nil {
			continue
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		// Load + verify in one read. The bytes passed to CompileModule
		// MUST be the bytes whose SHA-256 was checked — otherwise:
		//   (a) ReadVerifiedWASM's containment + size + regular-file
		//       guards (workdirpath.OpenRoot for path confinement,
		//       openRootRegularPackageFile for symlink/special-file
		//       rejection, readLimitedPackageFile for the size cap)
		//       don't gate the compile path; an os.ReadFile would
		//       follow a symlink to /dev/zero or pull an oversized
		//       file straight into memory before any check applied;
		//   (b) a TOCTOU between two reads lets an attacker swap the
		//       file after the verify and have CompileModule parse
		//       different bytes.
		// Codex #074 caught the previous two-read pattern where
		// os.ReadFile happened first (unverified) and ReadVerifiedWASM
		// was called purely for its error effect with the return
		// discarded; CompileModule then compiled the first (unverified)
		// bytes. Single verified read is the only correct shape.
		verifiedBytes, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath)
		if err != nil {
			continue
		}
		compiled, err := rt.CompileModule(ctx, verifiedBytes)
		if err != nil {
			issues = append(issues, ABIIssue{
				Plugin:       name,
				Version:      version,
				CompileError: err.Error(),
			})
			continue
		}
		exports := compiled.ExportedFunctions()
		var missingExports []string
		if _, ok := exports["stado_alloc"]; !ok {
			missingExports = append(missingExports, "stado_alloc")
		}
		if _, ok := exports["stado_free"]; !ok {
			missingExports = append(missingExports, "stado_free")
		}
		for _, def := range mf.Tools {
			expName := "stado_tool_" + def.Name
			if _, ok := exports[expName]; !ok {
				missingExports = append(missingExports, expName)
			}
		}

		var removedImports []string
		if provided != nil {
			seen := map[string]bool{}
			for _, imp := range compiled.ImportedFunctions() {
				modName, fnName, _ := imp.Import()
				if modName != pluginRuntime.NamespaceStado {
					continue
				}
				if !provided[fnName] && !seen[fnName] {
					removedImports = append(removedImports, fnName)
					seen[fnName] = true
				}
			}
			sort.Strings(removedImports)
		}
		_ = compiled.Close(ctx)
		if len(missingExports) > 0 || len(removedImports) > 0 {
			sort.Strings(missingExports)
			issues = append(issues, ABIIssue{
				Plugin:             name,
				Version:            version,
				MissingExports:     missingExports,
				RemovedHostImports: removedImports,
			})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Plugin == issues[j].Plugin {
			return issues[i].Version < issues[j].Version
		}
		return issues[i].Plugin < issues[j].Plugin
	})
	return issues, nil
}
