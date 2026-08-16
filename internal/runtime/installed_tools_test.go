package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// TestInstalledPluginTool_NameAndDescription: the wrapper exposes
// the manifest's tool name and description without loading wasm.
func TestInstalledPluginTool_NameAndDescription(t *testing.T) {
	mf := plugins.Manifest{
		Name:    "test-plugin",
		Version: "v0.1.0",
		Tools: []plugins.ToolDef{{
			Name:         "lookup",
			Description:  "Lookup a thing",
			Schema:       `{"type":"object"}`,
			Capabilities: plugins.CapabilitySubset(),
		}},
	}
	tl := newInstalledPluginTool(mf, plugins.RuntimeIdentity{}, mf.Tools[0], "/nonexistent/wasm/path", "", tool.ClassNonMutating, nil, nil)
	if tl.Name() != "lookup" {
		t.Errorf("Name() = %q, want lookup", tl.Name())
	}
	if tl.Description() != "Lookup a thing" {
		t.Errorf("Description() = %q, want 'Lookup a thing'", tl.Description())
	}
	// Schema returns parsed map; just verify it's non-nil.
	if tl.Schema() == nil {
		t.Error("Schema() returned nil")
	}
}

func TestInstalledToolInvocationRechecksRevokedAdmissionAfterRegistryBuild(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkg := installOverridePlugin(t, cfg, priv, pub, "live-admission", plugins.ToolDef{Name: "receipt__live"})
	reg := tools.NewRegistry()
	registerInstalledPluginTools(reg, cfg)
	tl, ok := reg.Get("receipt__live")
	if !ok {
		t.Fatal("admitted installed tool was not registered")
	}
	if err := plugins.RemoveInstallReceipt(cfg.StateDir(), filepath.Dir(pkg.Dir), pkg.Record.StoreKey); err != nil {
		t.Fatal(err)
	}
	result, err := tl.Run(context.Background(), []byte(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "exact install receipt") || !strings.Contains(result.Error, "exact install receipt") {
		t.Fatalf("post-registry receipt revocation result=%+v err=%v", result, err)
	}
}

// TestInstalledPluginTool_RunDispatchesViaPluginrun: Step 0.1 changed
// installedPluginTool.Run from a sentinel-error returner to a real
// invoker that dispatches via pluginrun.Run. Verify the new contract:
// the call returns a real error when prerequisites aren't met (here,
// the wasm path doesn't exist on disk so verification fails), not the
// pre-Step-0.1 "not invokable directly" sentinel string.
func TestInstalledPluginTool_RunDispatchesViaPluginrun(t *testing.T) {
	mf := plugins.Manifest{
		Name: "test-plugin", Version: "v0.1.0",
		Tools: []plugins.ToolDef{{Name: "lookup", Capabilities: plugins.CapabilitySubset()}},
	}
	tl := newInstalledPluginTool(mf, plugins.RuntimeIdentity{}, mf.Tools[0], "/nonexistent", "", tool.ClassNonMutating, nil, nil)
	res, err := tl.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Run() with nonexistent wasm path should error")
	}
	if res.Error == "" {
		t.Error("Run() should populate Result.Error alongside the returned error")
	}
	// The pre-Step-0.1 sentinel string MUST NOT appear — its presence
	// would mean we're back to the broken state where agent loop / MCP
	// server callers silently fail for installed plugins.
	if got := res.Error; strings.Contains(got, "not invokable directly via Tool.Run") {
		t.Errorf("Result.Error still contains pre-Step-0.1 sentinel string: %q", got)
	}
}

// TestRegisterInstalledPluginTools_NoPluginsDirNoOp: registry stays
// empty when nothing is installed.
func TestRegisterInstalledPluginTools_NoPluginsDirNoOp(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	reg := tools.NewRegistry()
	registerInstalledPluginTools(reg, cfg)
	if got := len(reg.All()); got != 0 {
		t.Errorf("expected empty registry; got %d tools", got)
	}
}

// TestRegisterInstalledPluginTools_NilCfgNoOp: nil config is a
// silent no-op (matches BuildDefaultRegistry's nil-cfg contract).
func TestRegisterInstalledPluginTools_NilCfgNoOp(t *testing.T) {
	reg := tools.NewRegistry()
	registerInstalledPluginTools(reg, nil)
	if got := len(reg.All()); got != 0 {
		t.Errorf("expected empty registry on nil cfg; got %d tools", got)
	}
}

func TestBuildRegistryWithPluginsFiltersAfterInstalledRegistration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled string
	}{
		{name: "exact installed-only name", enabled: "shell_create"},
		{name: "ordinary underscore glob", enabled: "shell_*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := isolatedRuntimeConfig(t)
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			installOverridePlugin(t, cfg, priv, pub, "persistent-shell", plugins.ToolDef{Name: "shell_create"})
			cfg.Tools.Enabled = []string{tc.enabled}

			reg, err := BuildRegistryWithPlugins(cfg)
			if err != nil {
				t.Fatalf("BuildRegistryWithPlugins: %v", err)
			}
			if _, ok := reg.Get("shell_create"); !ok {
				t.Fatalf("installed tool was filtered before registration for enabled=%q", tc.enabled)
			}
			for _, registered := range reg.All() {
				if !ToolMatchesGlob(registered.Name(), tc.enabled) {
					t.Fatalf("registry retained %q outside enabled glob %q", registered.Name(), tc.enabled)
				}
			}
		})
	}
}

func TestInstalledModuleForToolRejectsNameOnlyStandIn(t *testing.T) {
	if _, _, _, ok := InstalledModuleForTool(namedStubTool("nope__missing")); ok {
		t.Error("name-only stand-in resolved as an installed module")
	}
}

func TestInstalledModuleForToolIsPerSelectedAdapterUnderConcurrency(t *testing.T) {
	const name = "same__name"
	definition := plugins.ToolDef{Name: name, Capabilities: plugins.CapabilitySubset()}
	makeRegistry := func(canonical, namespace, path string) *tools.Registry {
		reg := tools.NewRegistry()
		manifest := plugins.Manifest{Name: namespace, Tools: []plugins.ToolDef{definition}}
		identity := plugins.RuntimeIdentity{Canonical: canonical, Namespace: namespace}
		reg.Register(newInstalledPluginTool(manifest, identity, definition, path, "", tool.ClassNonMutating, nil, reg))
		return reg
	}
	regA := makeRegistry("github.com/acme/a@v1", "github.com/acme/a", "/isolated/a.wasm")
	regB := makeRegistry("github.com/acme/b@v1", "github.com/acme/b", "/isolated/b.wasm")

	const callers = 64
	var wg sync.WaitGroup
	errs := make(chan string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(useA bool) {
			defer wg.Done()
			reg, wantNamespace, wantPath := regB, "github.com/acme/b", "/isolated/b.wasm"
			if useA {
				reg, wantNamespace, wantPath = regA, "github.com/acme/a", "/isolated/a.wasm"
			}
			selected, ok := reg.Get(name)
			if !ok {
				errs <- "selected tool disappeared"
				return
			}
			_, identity, path, ok := InstalledModuleForTool(selected)
			if !ok || identity.Namespace != wantNamespace || path != wantPath {
				errs <- fmt.Sprintf("resolved namespace=%q path=%q ok=%t", identity.Namespace, path, ok)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestInstalledToolCollisionRejectsEveryAuthenticatedSourceIndependentOfOrder(t *testing.T) {
	packages := []admittedInstalledPackage{
		{
			packageInfo: plugins.InstalledPackage{Manifest: plugins.Manifest{Name: "one", Tools: []plugins.ToolDef{{Name: "shared__tool", Capabilities: plugins.CapabilitySubset()}}}},
			identity:    plugins.RuntimeIdentity{Canonical: "github.com/acme/one@v1.0.0", Namespace: "github.com/acme/one"},
			wasmPath:    "/unused/one.wasm",
		},
		{
			packageInfo: plugins.InstalledPackage{Manifest: plugins.Manifest{Name: "two", Tools: []plugins.ToolDef{{Name: "shared__tool", Capabilities: plugins.CapabilitySubset()}}}},
			identity:    plugins.RuntimeIdentity{Canonical: "github.com/acme/two@v1.0.0", Namespace: "github.com/acme/two"},
			wasmPath:    "/unused/two.wasm",
		},
	}
	for _, order := range [][]admittedInstalledPackage{packages, {packages[1], packages[0]}} {
		reg := tools.NewRegistry()
		registerAdmittedInstalledTools(reg, &config.Config{}, order)
		if _, ok := reg.Get("shared__tool"); ok {
			t.Fatal("cross-source collision acquired registry authority")
		}
	}
}

func TestInstalledToolCollisionWithExistingOwnerRejectsWholePackageIndependentOfOrder(t *testing.T) {
	packages := []admittedInstalledPackage{
		{
			packageInfo: plugins.InstalledPackage{Manifest: plugins.Manifest{Name: "collision", Tools: []plugins.ToolDef{
				{Name: "existing__tool", Capabilities: plugins.CapabilitySubset()},
				{Name: "must__not_partially_register", Capabilities: plugins.CapabilitySubset()},
			}}},
			identity: plugins.RuntimeIdentity{Canonical: "github.com/acme/collision@v1.0.0", Namespace: "github.com/acme/collision"},
			wasmPath: "/unused/collision.wasm",
		},
		{
			packageInfo: plugins.InstalledPackage{Manifest: plugins.Manifest{Name: "clean", Tools: []plugins.ToolDef{{Name: "clean__tool", Capabilities: plugins.CapabilitySubset()}}}},
			identity:    plugins.RuntimeIdentity{Canonical: "github.com/acme/clean@v1.0.0", Namespace: "github.com/acme/clean"},
			wasmPath:    "/unused/clean.wasm",
		},
	}
	for _, order := range [][]admittedInstalledPackage{packages, {packages[1], packages[0]}} {
		reg := tools.NewRegistry()
		existing := namedStubTool("existing__tool")
		reg.Register(existing)
		registerAdmittedInstalledTools(reg, &config.Config{}, order)
		got, ok := reg.Get("existing__tool")
		if !ok || got != existing {
			t.Fatalf("existing registry owner was replaced: got=%T/%v present=%t", got, got, ok)
		}
		if _, ok := reg.Get("must__not_partially_register"); ok {
			t.Fatal("a package with an existing-owner collision registered a partial tool surface")
		}
		if _, ok := reg.Get("clean__tool"); !ok {
			t.Fatal("unrelated collision-free package was not registered")
		}
	}
}

func TestInstalledToolsClassifyWithSelectedCapabilitySubset(t *testing.T) {
	manifest := plugins.Manifest{
		Name:         "tool-registry",
		Capabilities: []string{"registry:catalog", "session:tool-surface"},
		Tools: []plugins.ToolDef{
			{Name: "tools__search", Class: "NonMutating", Capabilities: plugins.CapabilitySubset("registry:catalog")},
			{Name: "tools__activate", Class: "NonMutating", Capabilities: plugins.CapabilitySubset("registry:catalog", "session:tool-surface")},
		},
	}
	reg := tools.NewRegistry()
	registerAdmittedInstalledTools(reg, &config.Config{}, []admittedInstalledPackage{{
		packageInfo: plugins.InstalledPackage{Manifest: manifest},
		identity:    plugins.RuntimeIdentity{Canonical: "github.com/foobarto/tool-registry@v0.1.0", Namespace: "github.com/foobarto/tool-registry"},
		wasmPath:    "/unused/tool-registry.wasm",
	}})
	if got := reg.ClassOf("tools__search"); got != tool.ClassNonMutating {
		t.Fatalf("search class = %v, want non-mutating", got)
	}
	if got := reg.ClassOf("tools__activate"); got != tool.ClassStateMutating {
		t.Fatalf("activate class = %v, want state-mutating", got)
	}
}

func TestAgentChildOnlyToolsRequireExactNarrowProjection(t *testing.T) {
	const researchNamespace = "github.com/foobarto/stado-plugins/research"
	manifest := plugins.Manifest{Name: "research", Tools: []plugins.ToolDef{
		{Name: "research__catalog", Class: "NonMutating", Capabilities: plugins.CapabilitySubset(), AgentChildOnly: true},
		{Name: "memory__research", Class: "StateMutating", Capabilities: plugins.CapabilitySubset()},
	}}
	admitted := []admittedInstalledPackage{{
		packageInfo: plugins.InstalledPackage{Manifest: manifest},
		identity:    plugins.RuntimeIdentity{Canonical: researchNamespace + "@v1.0.0", Namespace: researchNamespace},
		wasmPath:    "/unused/research.wasm",
	}}
	for _, tc := range []struct {
		name     string
		exact    map[string]bool
		owner    string
		wantOpen bool
	}{
		{name: "ordinary parent"},
		{name: "child without narrow tools", exact: map[string]bool{}, owner: researchNamespace},
		{name: "unrelated exact narrow list", exact: map[string]bool{"research__search": true}, owner: researchNamespace},
		{name: "direct spawn guessing helper", exact: map[string]bool{"research__catalog": true}},
		{name: "bundled agent guessing helper", exact: map[string]bool{"research__catalog": true}, owner: "stado.dev/bundled/agent"},
		{name: "unrelated plugin guessing helper", exact: map[string]bool{"research__catalog": true}, owner: "github.com/other/plugin"},
		{name: "research outer exact helper", exact: map[string]bool{"research__catalog": true}, owner: researchNamespace, wantOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			registerAdmittedInstalledToolsForSurface(reg, &config.Config{}, admitted, tc.exact, tc.owner)
			_, got := reg.Get("research__catalog")
			if got != tc.wantOpen {
				t.Fatalf("child-only registration=%t, want %t", got, tc.wantOpen)
			}
			if _, ok := reg.Get("memory__research"); !ok {
				t.Fatal("ordinary outer tool disappeared with child-only projection")
			}
		})
	}

	reg := tools.NewRegistry()
	registerAdmittedInstalledToolsForSurface(reg, &config.Config{}, admitted, map[string]bool{"research__catalog": true}, researchNamespace)
	exec := &tools.Executor{Registry: reg}
	if _, _, err := configureSubagentTools(subagent.Request{Mode: subagent.DefaultMode, Role: subagent.DefaultRole, ToolProfile: "read_only", NarrowTools: []string{"research__catalog"}}, exec, nil); err != nil {
		t.Fatalf("matching read_only child projection: %v", err)
	}
	if _, ok := reg.Get("research__catalog"); !ok {
		t.Fatal("read_only projection removed exact non-mutating child tool")
	}
	childSelected, _ := reg.Get("research__catalog")
	_, childIdentity, childPath, ok := InstalledModuleForTool(childSelected)
	if !ok || childIdentity.Namespace != researchNamespace || childPath != "/unused/research.wasm" {
		t.Fatalf("exact child adapter resolution identity=%+v path=%q ok=%t", childIdentity, childPath, ok)
	}
	if _, _, _, ok := InstalledModuleForTool(namedStubTool("research__catalog")); ok {
		t.Fatal("name-only parent stand-in resolved the child-only module")
	}

	missing := &tools.Executor{Registry: tools.NewRegistry()}
	if _, _, err := configureSubagentTools(subagent.Request{Mode: subagent.DefaultMode, Role: subagent.DefaultRole, ToolProfile: "read_only", NarrowTools: []string{"research__catalog"}}, missing, nil); err == nil || !strings.Contains(err.Error(), "unavailable in the authenticated child registry") {
		t.Fatalf("missing exact child tool did not fail closed: %v", err)
	}
}

// Codex C4/Q P2 regression: pre-fix the package globals
// installedRunCfg + installedInvokeReg were re-bound by every
// BuildDefaultRegistry / registerInstalledPluginTools call. When
// `/tool info` triggered an unfiltered registry build, the next
// stado_tool_invoke from any plugin saw the freshly-rebound
// unfiltered globals — silently widening that plugin's nested-invoke
// surface past whatever [tools].enabled / .disabled scoped on the
// session's actual registry.
//
// After fix: each bundled tool (and each pluginOverrideTool) holds
// its own cfg + invokeReg pointers, set at build time by
// BuildDefaultRegistry / ApplyToolOverrides. Two BuildDefaultRegistry
// calls produce two independent sets of bundled tools, each pointing
// at the registry that built them. This test verifies that invariant.
func TestBundledPluginTool_PerBuildRuntimeIsolation(t *testing.T) {
	// Copilot review #63: use isolated XDG dirs so registerInstalled-
	// PluginTools doesn't scan the developer's real plugin install
	// (which could shadow fs__read on CI runners with state from
	// prior installs). isolatedRuntimeConfig sets up clean XDG_*
	// roots; we then customize Tools.Enabled per cfg without reaching
	// back into the user's installed plugins.
	cfgA := isolatedRuntimeConfig(t)
	cfgA.Tools.Enabled = []string{"fs.read"}
	cfgB := isolatedRuntimeConfig(t)
	cfgB.Tools.Enabled = []string{"shell.bash"}

	regA := BuildDefaultRegistry(cfgA)
	regB := BuildDefaultRegistry(cfgB)

	// Pick the same tool name from both registries — fs__read is
	// bundled, so it's a bundledPluginTool in both regs.
	tA, okA := regA.Get("fs__read")
	tB, okB := regB.Get("fs__read")
	if !okA || !okB {
		t.Fatalf("fs__read missing: A=%v B=%v", okA, okB)
	}

	// Bundled tools expose the manifest-owned wire name directly. Compare
	// the per-build adapter state to make sure registry ownership is local.
	bA := unwrapBundled(t, tA)
	bB := unwrapBundled(t, tB)

	// Per-build isolation: each tool's invokeReg points at the
	// registry that built it, not the most-recently-built registry.
	if bA.invokeReg != regA {
		t.Errorf("regA's fs__read.invokeReg should be regA; got %p (want %p)", bA.invokeReg, regA)
	}
	if bB.invokeReg != regB {
		t.Errorf("regB's fs__read.invokeReg should be regB; got %p (want %p)", bB.invokeReg, regB)
	}
	// Defensive: regA and regB are different pointers, so the two
	// invokeRegs must differ. If they don't, the global is back.
	if bA.invokeReg == bB.invokeReg {
		t.Error("regA and regB bundled tools share an invokeReg — package globals reintroduced")
	}

	// Same per-tool cfg isolation. cfgA and cfgB are distinct
	// pointers; the per-build tools must hold their own.
	if bA.cfg != cfgA {
		t.Errorf("regA's fs__read.cfg should be cfgA; got %p", bA.cfg)
	}
	if bB.cfg != cfgB {
		t.Errorf("regB's fs__read.cfg should be cfgB; got %p", bB.cfg)
	}
}

// unwrapBundled asserts that a core registry entry is the generic adapter.
func unwrapBundled(t *testing.T, tl tool.Tool) *bundledPluginTool {
	t.Helper()
	b, ok := tl.(*bundledPluginTool)
	if !ok {
		t.Fatalf("expected *bundledPluginTool after unwrap, got %T", tl)
	}
	return b
}
