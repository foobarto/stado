package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

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
// MissingExports lists the wasm-side exports the runtime expected but the
// plugin doesn't provide. IncompatibleExports lists exports that have the
// right name but the wrong WebAssembly function signature.
//
// RemovedHostImports lists the host-side imports the plugin expects from the
// "stado" namespace but that the runtime no longer provides.
// UnavailableHostImports lists imports the current host implements but does
// not expose under this plugin's signed capability declaration.
// IncompatibleHostImports lists imports whose names still exist but whose
// signatures no longer match the current host ABI.
// This is the v0.45.0 / D1 case — plugins compiled against v0.44.x
// import stado_fs_tool_read / stado_fs_tool_write etc., which were
// deleted in EP-no-internal-tools Step 7. Pre-fix these failed
// silently at instantiate time during the first tool call; the eager
// verifier surfaces them at session/new instead.
type ABIIssue struct {
	Plugin                  string
	Version                 string
	MissingExports          []string
	IncompatibleExports     []ABIFunctionMismatch
	RemovedHostImports      []string
	UnavailableHostImports  []string
	IncompatibleHostImports []ABIFunctionMismatch
	CompileError            string
}

// ABIFunctionMismatch preserves both sides of a same-name ABI mismatch so an
// operator can repair the guest declaration without reverse-engineering a
// wazero link error.
type ABIFunctionMismatch struct {
	Function string
	Expected string
	Actual   string
}

func (m ABIFunctionMismatch) String() string {
	return fmt.Sprintf("%s: got %s, want %s", m.Function, m.Actual, m.Expected)
}

// HasProblems reports whether the issue contains any incompatibility.
func (i ABIIssue) HasProblems() bool {
	return i.CompileError != "" || len(i.MissingExports) > 0 ||
		len(i.IncompatibleExports) > 0 || len(i.RemovedHostImports) > 0 ||
		len(i.UnavailableHostImports) > 0 || len(i.IncompatibleHostImports) > 0
}

// Missing is a back-compat alias for the union of MissingExports and
// RemovedHostImports — older callers (tests, log formatters) treated
// missing items as a single bag. New code should branch on the two
// fields separately so the operator-facing message can distinguish
// "rebuild — old plugin uses removed imports" from "rebuild — new
// tool was added to manifest but stado_tool_<name> isn't exported".
func (i ABIIssue) Missing() []string {
	if len(i.MissingExports) == 0 && len(i.RemovedHostImports) == 0 &&
		len(i.UnavailableHostImports) == 0 && len(i.IncompatibleExports) == 0 &&
		len(i.IncompatibleHostImports) == 0 {
		return nil
	}
	out := make([]string, 0, len(i.MissingExports)+len(i.RemovedHostImports)+len(i.UnavailableHostImports)+
		len(i.IncompatibleExports)+len(i.IncompatibleHostImports))
	out = append(out, i.MissingExports...)
	for _, name := range i.RemovedHostImports {
		out = append(out, "host:"+name)
	}
	for _, name := range i.UnavailableHostImports {
		out = append(out, "host-capability:"+name)
	}
	for _, mismatch := range i.IncompatibleExports {
		out = append(out, "signature:"+mismatch.Function)
	}
	for _, mismatch := range i.IncompatibleHostImports {
		out = append(out, "host-signature:"+mismatch.Function)
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
	if len(i.UnavailableHostImports) > 0 {
		parts = append(parts, fmt.Sprintf("imports unavailable under the signed manifest capabilities: %v", i.UnavailableHostImports))
	}
	if len(i.IncompatibleHostImports) > 0 {
		parts = append(parts, fmt.Sprintf("host import signature mismatches: %s (rebuild required)", formatABIFunctionMismatches(i.IncompatibleHostImports)))
	}
	if len(i.MissingExports) > 0 {
		parts = append(parts, fmt.Sprintf("missing exports: %v", i.MissingExports))
	}
	if len(i.IncompatibleExports) > 0 {
		parts = append(parts, fmt.Sprintf("export signature mismatches: %s", formatABIFunctionMismatches(i.IncompatibleExports)))
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

func formatABIFunctionMismatches(mismatches []ABIFunctionMismatch) string {
	parts := make([]string, len(mismatches))
	for index, mismatch := range mismatches {
		parts[index] = mismatch.String()
	}
	return strings.Join(parts, ", ")
}

type abiFunctionSignature struct {
	params  []api.ValueType
	results []api.ValueType
}

func signatureOf(definition api.FunctionDefinition) abiFunctionSignature {
	return abiFunctionSignature{
		params:  append([]api.ValueType(nil), definition.ParamTypes()...),
		results: append([]api.ValueType(nil), definition.ResultTypes()...),
	}
}

func (s abiFunctionSignature) equal(other abiFunctionSignature) bool {
	return slices.Equal(s.params, other.params) && slices.Equal(s.results, other.results)
}

func (s abiFunctionSignature) String() string {
	formatTypes := func(types []api.ValueType) string {
		if len(types) == 0 {
			return "()"
		}
		names := make([]string, len(types))
		for index, valueType := range types {
			names[index] = api.ValueTypeName(valueType)
		}
		return "(" + strings.Join(names, ", ") + ")"
	}
	return formatTypes(s.params) + " -> " + formatTypes(s.results)
}

func abiSignature(params, results []api.ValueType) abiFunctionSignature {
	return abiFunctionSignature{params: params, results: results}
}

// providedHostImportSignatures returns the stado-namespace functions exposed
// under one signed manifest's capability shape. It spins up a throwaway host,
// installs the real import module, and enumerates exact definitions. Most
// imports always link and deny at call time; cfg:state_dir deliberately omits
// its symbol unless the manifest declares that capability.
func providedHostImportSignatures(ctx context.Context, manifest plugins.Manifest) (map[string]abiFunctionSignature, error) {
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify rt: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()

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
	out := map[string]abiFunctionSignature{}
	for name, definition := range mod.ExportedFunctionDefinitions() {
		out[name] = signatureOf(definition)
	}
	return out, nil
}

// providedHostImports retains the older name-set seam used by focused tests
// while the compatibility checker consumes exact signatures.
func providedHostImports(ctx context.Context) (map[string]bool, error) {
	signatures, err := providedHostImportSignatures(ctx, allHostImportsManifest())
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(signatures))
	for name := range signatures {
		out[name] = true
	}
	return out, nil
}

type pluginABIVerifier struct {
	runtime     wazero.Runtime
	allProvided map[string]abiFunctionSignature
}

func newPluginABIVerifier(ctx context.Context) (*pluginABIVerifier, error) {
	provided, err := providedHostImportSignatures(ctx, allHostImportsManifest())
	if err != nil {
		return nil, err
	}
	return &pluginABIVerifier{
		runtime:     wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig()),
		allProvided: provided,
	}, nil
}

func allHostImportsManifest() plugins.Manifest {
	// cfg:state_dir is the sole host import whose symbol is conditionally
	// registered instead of always linking and denying at call time. Keep the
	// complete-contract manifest explicit so a missing signed capability can be
	// distinguished from a host import that was actually removed.
	return plugins.Manifest{
		Name: "abi-verifier", Version: "0.0.0",
		Capabilities: []string{"cfg:state_dir"},
	}
}

func (v *pluginABIVerifier) close(ctx context.Context) {
	if v != nil && v.runtime != nil {
		_ = v.runtime.Close(ctx)
	}
}

func (v *pluginABIVerifier) check(ctx context.Context, plugin string, manifest *plugins.Manifest, wasmBytes []byte) (ABIIssue, error) {
	issue := ABIIssue{Plugin: plugin, Version: manifest.Version}
	compiled, err := v.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		issue.CompileError = err.Error()
		return issue, nil
	}
	defer func() { _ = compiled.Close(ctx) }()
	provided, err := providedHostImportSignatures(ctx, *manifest)
	if err != nil {
		return ABIIssue{}, fmt.Errorf("enumerate host imports for %s: %w", plugin, err)
	}

	exports := compiled.ExportedFunctions()
	expectedExports := requiredInstalledPluginExportSignatures(manifest)
	for _, name := range requiredInstalledPluginExports(manifest) {
		definition, ok := exports[name]
		if !ok {
			issue.MissingExports = append(issue.MissingExports, name)
			continue
		}
		actual := signatureOf(definition)
		expected := expectedExports[name]
		if !actual.equal(expected) {
			issue.IncompatibleExports = append(issue.IncompatibleExports, ABIFunctionMismatch{
				Function: name, Expected: expected.String(), Actual: actual.String(),
			})
		}
	}
	// Tick is optional for lifecycle applications, but when present its
	// signature is part of the callable host contract.
	if definition, ok := exports["stado_plugin_tick"]; ok {
		expected := abiSignature(nil, []api.ValueType{api.ValueTypeI32})
		actual := signatureOf(definition)
		if !actual.equal(expected) {
			issue.IncompatibleExports = append(issue.IncompatibleExports, ABIFunctionMismatch{
				Function: "stado_plugin_tick", Expected: expected.String(), Actual: actual.String(),
			})
		}
	}

	seenRemoved := map[string]bool{}
	seenMismatch := map[string]bool{}
	for _, imported := range compiled.ImportedFunctions() {
		moduleName, functionName, _ := imported.Import()
		if moduleName != pluginRuntime.NamespaceStado {
			continue
		}
		expected, ok := provided[functionName]
		if !ok {
			if _, existsForAnotherCapabilitySet := v.allProvided[functionName]; existsForAnotherCapabilitySet {
				if !slices.Contains(issue.UnavailableHostImports, functionName) {
					issue.UnavailableHostImports = append(issue.UnavailableHostImports, functionName)
				}
			} else if !seenRemoved[functionName] {
				issue.RemovedHostImports = append(issue.RemovedHostImports, functionName)
				seenRemoved[functionName] = true
			}
			continue
		}
		actual := signatureOf(imported)
		if !actual.equal(expected) && !seenMismatch[functionName] {
			issue.IncompatibleHostImports = append(issue.IncompatibleHostImports, ABIFunctionMismatch{
				Function: functionName, Expected: expected.String(), Actual: actual.String(),
			})
			seenMismatch[functionName] = true
		}
	}
	sort.Strings(issue.MissingExports)
	sort.Strings(issue.RemovedHostImports)
	sort.Strings(issue.UnavailableHostImports)
	sort.Slice(issue.IncompatibleExports, func(i, j int) bool {
		return issue.IncompatibleExports[i].Function < issue.IncompatibleExports[j].Function
	})
	sort.Slice(issue.IncompatibleHostImports, func(i, j int) bool {
		return issue.IncompatibleHostImports[i].Function < issue.IncompatibleHostImports[j].Function
	})
	return issue, nil
}

// CheckPluginPackageABI checks one unpacked plugin directory against the exact
// host ABI compiled into this stado source tree. It validates the manifest,
// verifies the wasm digest, compiles the module without executing guest code,
// and compares required exports plus every stado host import by name and
// signature. It does not authenticate the manifest signature; callers that
// admit a package must perform the normal trust verification separately.
func CheckPluginPackageABI(ctx context.Context, dir string) (ABIIssue, error) {
	manifest, _, err := plugins.LoadFromDir(dir)
	if err != nil {
		return ABIIssue{}, err
	}
	wasmBytes, err := plugins.ReadVerifiedWASM(manifest.WASMSHA256, filepath.Join(dir, "plugin.wasm"))
	if err != nil {
		return ABIIssue{}, err
	}
	verifier, err := newPluginABIVerifier(ctx)
	if err != nil {
		return ABIIssue{}, err
	}
	defer verifier.close(ctx)
	return verifier.check(ctx, manifest.Name, manifest, wasmBytes)
}

// VerifyInstalledPluginsABI eagerly checks every installed-and-active
// plugin in cfg.StateDir()/plugins/ against the runtime ABI:
//
//  1. wasm-side exports: stado_alloc, stado_free, lifecycle callbacks, and one
//     stado_tool_<name> export per ToolDef must exist with exact signatures.
//  2. host-side imports: every function the plugin imports from the "stado"
//     namespace must exist with the exact signature the host provides.
//     This catches plugins built against an older runtime that
//     reference host functions deleted in this version (e.g. v0.44.x
//     plugins importing stado_fs_tool_read after Step 7 removed it).
//
// Returns the issue set (empty when everything compiles, exports
// cleanly, AND only imports current host functions). Caller decides
// what to do with issues — surface them as a session/new error,
// log + continue, etc.
//
// Cost: one complete host-import enumeration, one capability-shaped
// enumeration, and one wazero.CompileModule per plugin. CompileModule decodes
// guest bytes without instantiating or executing them.
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
	packages, err := plugins.ListInstalledPackages(pluginsDir)
	if err != nil {
		return nil, fmt.Errorf("enumerate installed plugins: %w", err)
	}
	if len(packages) == 0 {
		return nil, nil
	}
	groups := make(map[string][]plugins.InstalledPackage)
	for _, pkg := range packages {
		groups[pkg.Identity.Namespace] = append(groups[pkg.Identity.Namespace], pkg)
	}

	verifier, err := newPluginABIVerifier(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize plugin ABI verifier: %w", err)
	}
	defer verifier.close(ctx)

	var issues []ABIIssue
	for namespace, candidates := range groups {
		selected, ok, selectErr := plugins.PickActivePackage(pluginsDir, namespace, candidates)
		if selectErr != nil || !ok {
			continue
		}
		dir, mf, sig := selected.Dir, &selected.Manifest, selected.Signature
		if err := VerifyInstalledPlugin(ctx, cfg, dir, mf, sig); err != nil {
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
		issue, err := verifier.check(ctx, selected.Identity.Canonical, mf, verifiedBytes)
		if err != nil {
			return nil, err
		}
		if issue.HasProblems() {
			issues = append(issues, issue)
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

func requiredInstalledPluginExports(manifest *plugins.Manifest) []string {
	exports := []string{"stado_alloc", "stado_free"}
	for _, def := range manifest.Tools {
		exports = append(exports, "stado_tool_"+def.ExportName())
	}
	if manifest.Lifecycle != nil && len(manifest.Lifecycle.Points) > 0 {
		exports = append(exports, "stado_plugin_lifecycle")
	}
	if manifest.Lifecycle != nil && len(manifest.Lifecycle.Events) > 0 {
		exports = append(exports, "stado_plugin_event")
	}
	if len(manifest.Commands) > 0 {
		exports = append(exports, "stado_plugin_command")
	}
	return exports
}

func requiredInstalledPluginExportSignatures(manifest *plugins.Manifest) map[string]abiFunctionSignature {
	i32 := api.ValueTypeI32
	out := map[string]abiFunctionSignature{
		"stado_alloc": abiSignature([]api.ValueType{i32}, []api.ValueType{i32}),
		"stado_free":  abiSignature([]api.ValueType{i32, i32}, nil),
	}
	callback := abiSignature([]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32})
	for _, def := range manifest.Tools {
		out["stado_tool_"+def.ExportName()] = callback
	}
	if manifest.Lifecycle != nil && len(manifest.Lifecycle.Points) > 0 {
		out["stado_plugin_lifecycle"] = callback
	}
	if manifest.Lifecycle != nil && len(manifest.Lifecycle.Events) > 0 {
		out["stado_plugin_event"] = callback
	}
	if len(manifest.Commands) > 0 {
		out["stado_plugin_command"] = callback
	}
	return out
}
