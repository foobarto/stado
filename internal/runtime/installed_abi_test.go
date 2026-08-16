package runtime

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

func TestVerifyInstalledPluginsABI_NilCfg(t *testing.T) {
	issues, err := VerifyInstalledPluginsABI(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil for nil cfg", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %v, want none", issues)
	}
}

func TestInstalledABIUsesSignedExportInsteadOfModelName(t *testing.T) {
	mf := &plugins.Manifest{Tools: []plugins.ToolDef{{Name: "friendly__lookup", Export: "lookup_v2"}}}
	got := requiredInstalledPluginExports(mf)
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "stado_tool_lookup_v2") {
		t.Fatalf("required exports = %v", got)
	}
	if strings.Contains(joined, "stado_tool_friendly__lookup") {
		t.Fatalf("ABI preflight retained name-derived export: %v", got)
	}
}

func TestVerifyInstalledPluginsABI_NoInstalls(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	issues, err := VerifyInstalledPluginsABI(context.Background(), cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %v, want none for empty plugins dir", issues)
	}
}

func TestABIIssue_StringFormat(t *testing.T) {
	cases := []struct {
		name   string
		issue  ABIIssue
		expect []string
	}{
		{
			name: "missing_exports",
			issue: ABIIssue{
				Plugin:         "demo",
				Version:        "0.1.0",
				MissingExports: []string{"stado_alloc", "stado_tool_run"},
			},
			expect: []string{"demo@0.1.0", "missing exports", "stado_alloc", "stado_tool_run"},
		},
		{
			name: "removed_host_imports",
			issue: ABIIssue{
				Plugin:             "htb-toolkit",
				Version:            "0.4.2",
				RemovedHostImports: []string{"stado_fs_tool_read", "stado_fs_tool_write"},
			},
			expect: []string{"htb-toolkit@0.4.2", "imports removed", "stado_fs_tool_read", "rebuild required"},
		},
		{
			name: "compile_error",
			issue: ABIIssue{
				Plugin:       "demo",
				Version:      "0.1.0",
				CompileError: "decoder: bad magic",
			},
			expect: []string{"demo@0.1.0", "wasm compile failed", "bad magic"},
		},
		{
			name: "signature_mismatches",
			issue: ABIIssue{
				Plugin:  "browser",
				Version: "0.2.1",
				IncompatibleHostImports: []ABIFunctionMismatch{{
					Function: "stado_net_dial", Actual: "(i32) -> (i32)", Expected: "(i32, i32) -> (i64)",
				}},
				IncompatibleExports: []ABIFunctionMismatch{{
					Function: "stado_alloc", Actual: "() -> (i32)", Expected: "(i32) -> (i32)",
				}},
			},
			expect: []string{"browser@0.2.1", "host import signature mismatches", "stado_net_dial", "export signature mismatches", "stado_alloc"},
		},
		{
			name: "capability_unavailable",
			issue: ABIIssue{
				Plugin:                 "state-dir-info",
				Version:                "0.1.1",
				UnavailableHostImports: []string{"stado_cfg_state_dir"},
			},
			expect: []string{"state-dir-info@0.1.1", "signed manifest capabilities", "stado_cfg_state_dir"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.issue.String()
			for _, want := range tc.expect {
				if !strings.Contains(got, want) {
					t.Errorf("String() = %q; missing %q", got, want)
				}
			}
		})
	}
}

// TestProvidedHostImports_HasCoreSet sanity-checks that the runtime's
// host-import set covers the expected primitives. Catches accidental
// removals (which would silently break the import-side ABI verifier
// from flagging plugins that depend on those imports).
func TestProvidedHostImports_HasCoreSet(t *testing.T) {
	provided, err := providedHostImports(context.Background())
	if err != nil {
		t.Fatalf("providedHostImports: %v", err)
	}
	wantPresent := []string{
		"stado_alloc", // ABI exports — wait, these are EXPORTS not imports
		"stado_log",
		"stado_cfg_state_dir",
		"stado_fs_read",
		"stado_fs_write",
		"stado_fs_last_error",
		"stado_exec",
		"stado_progress",
	}
	for _, n := range wantPresent {
		if n == "stado_alloc" {
			// stado_alloc is a wasm-EXPORT (plugin → host), never a host-provided
			// function. Skip — this is just guarding against me misreading the API.
			if provided[n] {
				t.Errorf("stado_alloc unexpectedly in providedHostImports — host should never expose it")
			}
			continue
		}
		if !provided[n] {
			t.Errorf("missing host import %q in provided set; runtime broke a primitive?", n)
		}
	}
	wantAbsent := []string{
		"stado_fs_tool_read",    // removed in Step 7
		"stado_fs_tool_write",   // removed in Step 7
		"stado_fs_tool_edit",    // removed in Step 7
		"stado_search_ripgrep",  // removed in Step 5
		"stado_search_ast_grep", // removed in Step 5
	}
	for _, n := range wantAbsent {
		if provided[n] {
			t.Errorf("removed import %q still in provided set", n)
		}
	}
}

func TestPluginABIVerifierAcceptsExactImportAndExportSignatures(t *testing.T) {
	verifier := newTestPluginABIVerifier(t)
	manifest := abiFixtureManifest()
	exports := abiFixtureRequiredExports(manifest)
	wasm := buildABIFixture(t, []abiFixtureFunction{{
		name: "stado_net_dial", signature: verifier.allProvided["stado_net_dial"],
	}}, exports)

	issue := checkABIFixture(t, verifier, "fixture", manifest, wasm)
	if issue.HasProblems() {
		t.Fatalf("exact current ABI rejected: %s", issue.String())
	}
}

func TestPluginABIVerifierRejectsSameNameWrongHostImportSignature(t *testing.T) {
	verifier := newTestPluginABIVerifier(t)
	manifest := abiFixtureManifest()
	legacyDial := abiSignature(
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32},
	)
	wasm := buildABIFixture(t, []abiFixtureFunction{{
		name: "stado_net_dial", signature: legacyDial,
	}}, abiFixtureRequiredExports(manifest))

	issue := checkABIFixture(t, verifier, "legacy-browser", manifest, wasm)
	if len(issue.IncompatibleHostImports) != 1 {
		t.Fatalf("host signature mismatches = %+v, want stado_net_dial", issue.IncompatibleHostImports)
	}
	mismatch := issue.IncompatibleHostImports[0]
	if mismatch.Function != "stado_net_dial" || !strings.Contains(mismatch.Expected, "i64") || !strings.Contains(mismatch.Actual, "i32") {
		t.Fatalf("wrong mismatch detail: %+v", mismatch)
	}
	if len(issue.RemovedHostImports) != 0 {
		t.Fatalf("same-name mismatch misreported as removed import: %v", issue.RemovedHostImports)
	}
}

func TestPluginABIVerifierRejectsRemovedHostImport(t *testing.T) {
	verifier := newTestPluginABIVerifier(t)
	manifest := abiFixtureManifest()
	wasm := buildABIFixture(t, []abiFixtureFunction{{
		name: "stado_pty_attach",
		signature: abiSignature(
			[]api.ValueType{api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32},
		),
	}}, abiFixtureRequiredExports(manifest))

	issue := checkABIFixture(t, verifier, "legacy-pty", manifest, wasm)
	if len(issue.RemovedHostImports) != 1 || issue.RemovedHostImports[0] != "stado_pty_attach" {
		t.Fatalf("removed imports = %v", issue.RemovedHostImports)
	}
}

func TestPluginABIVerifierDistinguishesMissingCapabilityFromRemovedImport(t *testing.T) {
	verifier := newTestPluginABIVerifier(t)
	manifest := abiFixtureManifest()
	wasm := buildABIFixture(t, []abiFixtureFunction{{
		name: "stado_cfg_state_dir", signature: verifier.allProvided["stado_cfg_state_dir"],
	}}, abiFixtureRequiredExports(manifest))

	issue := checkABIFixture(t, verifier, "missing-capability", manifest, wasm)
	if len(issue.UnavailableHostImports) != 1 || issue.UnavailableHostImports[0] != "stado_cfg_state_dir" {
		t.Fatalf("unavailable imports = %v", issue.UnavailableHostImports)
	}
	if len(issue.RemovedHostImports) != 0 {
		t.Fatalf("capability-gated import misreported as removed: %v", issue.RemovedHostImports)
	}

	manifest.Capabilities = []string{"cfg:state_dir"}
	issue = checkABIFixture(t, verifier, "capability-granted", manifest, wasm)
	if issue.HasProblems() {
		t.Fatalf("signed cfg:state_dir capability did not expose its import: %s", issue.String())
	}
}

func TestPluginABIVerifierRejectsWrongRequiredExportSignature(t *testing.T) {
	verifier := newTestPluginABIVerifier(t)
	manifest := abiFixtureManifest()
	exports := abiFixtureRequiredExports(manifest)
	for index := range exports {
		if exports[index].name == "stado_alloc" {
			exports[index].signature = abiSignature(nil, []api.ValueType{api.ValueTypeI32})
		}
	}
	wasm := buildABIFixture(t, nil, exports)

	issue := checkABIFixture(t, verifier, "bad-allocator", manifest, wasm)
	if len(issue.IncompatibleExports) != 1 || issue.IncompatibleExports[0].Function != "stado_alloc" {
		t.Fatalf("export signature mismatches = %+v", issue.IncompatibleExports)
	}
}

func TestPluginABIVerifierRequiresManifestDeclaredLifecycleCallbacks(t *testing.T) {
	verifier := newTestPluginABIVerifier(t)
	manifest := abiFixtureManifest()
	manifest.Lifecycle = &plugins.LifecycleDef{
		Points: []string{"pre_llm"},
		Events: []string{"timer.due"},
	}
	manifest.Commands = []plugins.CommandDef{{Name: "status"}}
	exports := abiFixtureRequiredExports(abiFixtureManifest()) // deliberately omit lifecycle exports
	wasm := buildABIFixture(t, nil, exports)

	issue := checkABIFixture(t, verifier, "incomplete-application", manifest, wasm)
	want := []string{"stado_plugin_command", "stado_plugin_event", "stado_plugin_lifecycle"}
	if !slicesEqual(issue.MissingExports, want) {
		t.Fatalf("missing lifecycle exports = %v, want %v", issue.MissingExports, want)
	}
}

func newTestPluginABIVerifier(t *testing.T) *pluginABIVerifier {
	t.Helper()
	verifier, err := newPluginABIVerifier(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { verifier.close(context.Background()) })
	return verifier
}

func checkABIFixture(t *testing.T, verifier *pluginABIVerifier, plugin string, manifest *plugins.Manifest, wasm []byte) ABIIssue {
	t.Helper()
	issue, err := verifier.check(context.Background(), plugin, manifest, wasm)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

func abiFixtureManifest() *plugins.Manifest {
	return &plugins.Manifest{
		Name: "fixture", Version: "1.0.0",
		Tools: []plugins.ToolDef{{Name: "fixture_run", Capabilities: plugins.CapabilitySubset()}},
	}
}

type abiFixtureFunction struct {
	name      string
	signature abiFunctionSignature
}

func abiFixtureRequiredExports(manifest *plugins.Manifest) []abiFixtureFunction {
	expected := requiredInstalledPluginExportSignatures(manifest)
	names := requiredInstalledPluginExports(manifest)
	out := make([]abiFixtureFunction, 0, len(names))
	for _, name := range names {
		out = append(out, abiFixtureFunction{name: name, signature: expected[name]})
	}
	return out
}

func buildABIFixture(t *testing.T, imports, exports []abiFixtureFunction) []byte {
	t.Helper()
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	all := append(append([]abiFixtureFunction(nil), imports...), exports...)

	typeSection := wasmU32(uint32(len(all)))
	for _, function := range all {
		typeSection = append(typeSection, 0x60)
		typeSection = append(typeSection, wasmValueTypes(function.signature.params)...)
		typeSection = append(typeSection, wasmValueTypes(function.signature.results)...)
	}
	module = appendWasmSection(module, 1, typeSection)

	if len(imports) > 0 {
		importSection := wasmU32(uint32(len(imports)))
		for index, function := range imports {
			importSection = append(importSection, wasmName("stado")...)
			importSection = append(importSection, wasmName(function.name)...)
			importSection = append(importSection, 0x00) // function import
			importSection = append(importSection, wasmU32(uint32(index))...)
		}
		module = appendWasmSection(module, 2, importSection)
	}

	functionSection := wasmU32(uint32(len(exports)))
	for index := range exports {
		functionSection = append(functionSection, wasmU32(uint32(len(imports)+index))...)
	}
	module = appendWasmSection(module, 3, functionSection)
	module = appendWasmSection(module, 5, []byte{0x01, 0x00, 0x01}) // one-page memory

	exportSection := wasmU32(uint32(len(exports) + 1))
	exportSection = append(exportSection, wasmName("memory")...)
	exportSection = append(exportSection, 0x02, 0x00)
	for index, function := range exports {
		exportSection = append(exportSection, wasmName(function.name)...)
		exportSection = append(exportSection, 0x00)
		exportSection = append(exportSection, wasmU32(uint32(len(imports)+index))...)
	}
	module = appendWasmSection(module, 7, exportSection)

	codeSection := wasmU32(uint32(len(exports)))
	for _, function := range exports {
		body := []byte{0x00} // no locals
		for _, result := range function.signature.results {
			switch result {
			case api.ValueTypeI32:
				body = append(body, 0x41, 0x00)
			case api.ValueTypeI64:
				body = append(body, 0x42, 0x00)
			default:
				t.Fatalf("unsupported fixture result type %s", api.ValueTypeName(result))
			}
		}
		body = append(body, 0x0b)
		codeSection = append(codeSection, wasmU32(uint32(len(body)))...)
		codeSection = append(codeSection, body...)
	}
	module = appendWasmSection(module, 10, codeSection)
	return module
}

func wasmValueTypes(types []api.ValueType) []byte {
	out := wasmU32(uint32(len(types)))
	for _, valueType := range types {
		out = append(out, byte(valueType))
	}
	return out
}

func appendWasmSection(module []byte, sectionID byte, payload []byte) []byte {
	module = append(module, sectionID)
	module = append(module, wasmU32(uint32(len(payload)))...)
	return append(module, payload...)
}

func wasmName(value string) []byte {
	out := wasmU32(uint32(len(value)))
	return append(out, value...)
}

func wasmU32(value uint32) []byte {
	var out []byte
	for {
		current := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			current |= 0x80
		}
		out = append(out, current)
		if value == 0 {
			return out
		}
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
