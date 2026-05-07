package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tetratelabs/wazero"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// ABIIssue describes a single installed-plugin ABI mismatch found by
// VerifyInstalledPluginsABI. The Plugin / Version pair identifies the
// failing install; Missing lists exports the runtime expected but
// couldn't find. CompileError captures decoder failures (truncated
// or malformed wasm); when set, Missing is unset.
type ABIIssue struct {
	Plugin       string
	Version      string
	Missing      []string
	CompileError string
}

// String formats an ABIIssue for human-readable error reporting.
func (i ABIIssue) String() string {
	if i.CompileError != "" {
		return fmt.Sprintf("%s@%s: wasm compile failed: %s", i.Plugin, i.Version, i.CompileError)
	}
	return fmt.Sprintf("%s@%s: missing exports: %v", i.Plugin, i.Version, i.Missing)
}

// VerifyInstalledPluginsABI eagerly checks every installed-and-active
// plugin in cfg.StateDir()/plugins/ against the runtime ABI: stado_alloc,
// stado_free, and one stado_tool_<name> export per ToolDef in the
// manifest. Returns the issue set (empty when everything compiles +
// exports cleanly). Caller decides what to do with issues — surface
// them as a session/new error, log + continue, etc.
//
// Cheap-ish: wazero.CompileModule decodes the module without
// instantiating (no host imports needed, no memory pages allocated for
// .data sections). Runs sequentially; install counts are typically
// O(10s) so total cost is sub-second.
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
		bytes, err := os.ReadFile(wasmPath)
		if err != nil {
			continue
		}
		if _, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath); err != nil {
			continue
		}
		compiled, err := rt.CompileModule(ctx, bytes)
		if err != nil {
			issues = append(issues, ABIIssue{
				Plugin:       name,
				Version:      version,
				CompileError: err.Error(),
			})
			continue
		}
		exports := compiled.ExportedFunctions()
		var missing []string
		if _, ok := exports["stado_alloc"]; !ok {
			missing = append(missing, "stado_alloc")
		}
		if _, ok := exports["stado_free"]; !ok {
			missing = append(missing, "stado_free")
		}
		for _, def := range mf.Tools {
			expName := "stado_tool_" + def.Name
			if _, ok := exports[expName]; !ok {
				missing = append(missing, expName)
			}
		}
		_ = compiled.Close(ctx)
		if len(missing) > 0 {
			sort.Strings(missing)
			issues = append(issues, ABIIssue{
				Plugin:  name,
				Version: version,
				Missing: missing,
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
