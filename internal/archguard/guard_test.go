// Package archguard contains source- and documentation-level architecture
// tests. The tests intentionally depend on repository layout: they protect
// boundaries that ordinary package tests cannot express without import cycles.
package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const walImportPath = "github.com/foobarto/stado/internal/broker/wal"
const authorityImportPath = "github.com/foobarto/stado/internal/broker/authority"

// walDebt is a shrinking allowlist of the pre-broker-migration production
// call sites. Missing entries are permitted so deleting or migrating a call
// needs no guard update; any additional call requires an explicit architecture
// decision. The target is an empty map: canonical state is submitted through
// typed broker services and only broker-owned code opens its WAL.
var walDebt = map[string]int{
	"cmd/stado/learning.go::newLearnMigrateCmd::OpenShared":                    1,
	"cmd/stado/learning.go::newLearnRetrievalReportCmd::OpenShared":            1,
	"cmd/stado/learning.go::openArtifactService::OpenShared":                   1,
	"cmd/stado/learning.go::runLearnReview::OpenShared":                        1,
	"cmd/stado/run_research.go::buildRunResearch::OpenShared":                  1,
	"cmd/stado/session_context.go::withSessionContext::OpenShared":             1,
	"internal/artifactprompt/context.go::Build::OpenShared":                    1,
	"internal/guidance/guidance.go::Build::OpenShared":                         1,
	"internal/runtime/retained_bridge.go::ConfigureRetainedBridge::OpenShared": 1,
	"internal/stateprompt/context.go::Build::OpenShared":                       1,
	"internal/trajectory/recorder.go::EnsureObjective::OpenShared":             1,
	"internal/trajectory/recorder.go::ToolOutcome::OpenShared":                 1,
	"internal/tui/learn_review.go::manageLearn::OpenShared":                    1,
	"internal/tui/learn_review.go::startLearnReview::OpenShared":               1,
	"internal/tui/model_stream.go::buildResearchRunner::OpenShared":            1,
	"internal/tui/supervise.go::beginSupervise::OpenShared":                    1,
	"internal/tui/supervise.go::loadSupervision::OpenShared":                   1,
}

// brokerOwnedWALCalls are composition-root opens made on behalf of the broker
// process itself. They are ownership, not migration debt, and remain exact so
// ordinary CLI/TUI call sites cannot inherit this exemption.
var brokerOwnedWALCalls = map[string]int{
	"cmd/stado/broker_bridge.go::buildBrokerService::OpenShared": 1,
}

// Grant issuance belongs to the broker authority composition root. In-process
// TUI/CLI code is part of the hostile orchestrator under EP-0050 and cannot
// turn its own callback into proof of operator intent. Keep this exact so a
// future trusted presenter must arrive as an explicit architecture change.
var brokerAuthorityConstructorCalls = map[string]int{
	"internal/broker/artifact.go::ConfigureArtifactStore::New": 1,
}

// nativeRegistrationDebt enumerates model-visible Go registrations that
// predate the WASM-only boundary. It may only shrink. Registrations produced
// by the verified plugin constructors are recognized separately; all other
// new registrations must move behind a WASM manifest instead of growing this
// list. The supervise registrations introduced by PR #257 are deliberately
// absent so this guard stays red until that application moves to WASM.
var nativeRegistrationDebt = map[string]int{
	"cmd/stado/mcp_server.go::<package>::Register":                     1,
	"internal/runtime/executor.go::BuildDefaultRegistry::Register":     2,
	"internal/runtime/executor.go::BuildRegistryWithPlugins::Register": 1,
	"internal/runtime/mcp_glue.go::attachMCP::Register":                1,
	"internal/runtime/meta_tools.go::registerMetaTools::Register":      9,
}

// nativeDefinitionDebt covers direct, provider-visible agent.ToolDef literals
// which bypass tools.Registry. The research tools are existing migration debt.
// Supervise reviewer tools are intentionally not allowlisted.
var nativeDefinitionDebt = map[string]int{
	"internal/research/research.go::toolDefs::corpus_catalog": 1,
	"internal/research/research.go::toolDefs::corpus_open":    1,
	"internal/research/research.go::toolDefs::corpus_search":  1,
}

// Handler-side behavior selected by a wire tool name is another native
// application surface even when registration happens elsewhere. These two
// pre-existing hooks may shrink; supervise interception is intentionally not
// permitted and must move into its lifecycle application.
var nativeHandlerDebt = map[string]int{
	"internal/tui/handler_tools.go::agent__spawn": 1,
	"internal/tui/handler_tools.go::skills__load": 1,
}

// Compatibility identity constructors are not valid production loaders: they
// have no authenticated source argument. This allowlist may only shrink. New
// execution paths must use NewHostWithIdentity plus a lock-, bundle-, or
// source-bound RuntimeIdentity.
var runtimeIdentityCompatibilityDebt = map[string]int{
	"internal/artifacts/learning.go::DefaultKindRegistry::RuntimeIdentityForBundled": 1,
	"internal/plugins/runtime/host.go::NewHost::RuntimeIdentityForLocal":             1,
}

func TestDirectBrokerWALOpensCannotGrow(t *testing.T) {
	root := repoRoot(t)
	actual := directWALCalls(t, root)
	for key, count := range actual {
		if count <= brokerOwnedWALCalls[key] {
			continue
		}
		allowed := walDebt[key]
		if count > allowed {
			t.Errorf("direct broker WAL call %s occurs %d time(s), allowed debt is %d; route canonical state through a typed broker service instead", key, count, allowed)
		}
	}
}

func TestOperatorGrantIssuerStaysBrokerPrivate(t *testing.T) {
	actual := importedFunctionCalls(t, repoRoot(t), authorityImportPath, map[string]bool{"New": true})
	assertShrinkingDebt(t, "operator-grant authority construction", actual, brokerAuthorityConstructorCalls)
}

func TestNativeModelToolRegistryRemainsEmpty(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "runtime", "bundled_plugin_tools.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "buildNativeRegistry" {
			continue
		}
		if nativeRegistryIsEmpty(fn) {
			return
		}
		t.Fatal("buildNativeRegistry must stay empty; model-facing behavior belongs in WASM and native Go exposes only generic host primitives")
	}
	t.Fatal("buildNativeRegistry not found; update this guard as part of the same architecture change")
}

func TestNativeModelToolSurfacesCannotGrow(t *testing.T) {
	root := repoRoot(t)
	registrations, definitions, handlers := nativeModelToolSurfaces(t, root)
	assertShrinkingDebt(t, "native model-tool registration", registrations, nativeRegistrationDebt)
	assertShrinkingDebt(t, "native agent.ToolDef", definitions, nativeDefinitionDebt)
	assertShrinkingDebt(t, "native tool-result interception", handlers, nativeHandlerDebt)
}

func TestCompatibilityPluginIdentityConstructionCannotGrow(t *testing.T) {
	actual := compatibilityPluginIdentityCalls(t, repoRoot(t))
	assertShrinkingDebt(t, "compatibility plugin identity construction", actual, runtimeIdentityCompatibilityDebt)
}

func TestControllerOnlyApplicationOperationsStayOutOfGuestMap(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "broker", "application_rpc.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"worker.get": true, "worker.activate": true, "worker.cancel-host": true}
	foundMap := false
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "applicationOperations" || len(value.Values) != 1 {
				continue
			}
			literal, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatal("applicationOperations is no longer a composite map literal; update this guard with the same architecture change")
			}
			foundMap = true
			for _, element := range literal.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := pair.Key.(*ast.BasicLit)
				if !ok || key.Kind != token.STRING {
					continue
				}
				operation, err := strconv.Unquote(key.Value)
				if err != nil {
					t.Fatal(err)
				}
				if forbidden[operation] {
					t.Errorf("controller-only operation %q is registered in the application-bearer operation map; use a controller-authenticated broker RPC", operation)
				}
			}
		}
	}
	if !foundMap {
		t.Fatal("applicationOperations not found; update this guard with the same architecture change")
	}
}

func compatibilityPluginIdentityCalls(t *testing.T, root string) map[string]int {
	t.Helper()
	actual := make(map[string]int)
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(filePath) != ".go" || strings.HasSuffix(filePath, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			return err
		}
		aliases := make(map[string]string)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			alias := importPath[strings.LastIndex(importPath, "/")+1:]
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			aliases[alias] = importPath
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				pkg, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				importPath := aliases[pkg.Name]
				if importPath == "github.com/foobarto/stado/internal/plugins/runtime" && fun.Sel.Name == "NewHost" {
					name = fun.Sel.Name
				}
				if importPath == "github.com/foobarto/stado/internal/plugins" &&
					(fun.Sel.Name == "RuntimeIdentityForLocal" || fun.Sel.Name == "RuntimeIdentityForBundled") {
					name = fun.Sel.Name
				}
			case *ast.Ident:
				if strings.HasPrefix(rel, "internal/plugins/runtime/") && fun.Name == "NewHost" {
					name = fun.Name
				}
				if strings.HasPrefix(rel, "internal/plugins/") &&
					(fun.Name == "RuntimeIdentityForLocal" || fun.Name == "RuntimeIdentityForBundled") {
					name = fun.Name
				}
			}
			if name != "" {
				key := rel + "::" + enclosingFunction(file, call.Pos()) + "::" + name
				actual[key]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return actual
}

func nativeRegistryIsEmpty(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) != 2 {
		return false
	}
	assign, ok := fn.Body.List[0].(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	name, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || name.Name != "r" {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewRegistry" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "tools" {
		return false
	}
	ret, ok := fn.Body.List[1].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	returned, ok := ret.Results[0].(*ast.Ident)
	return ok && returned.Name == "r"
}

// These are current product documents, not historical EPs or release notes.
// The expressions reject positive/deferred support claims while still allowing
// an explicit statement that macOS and Windows are unsupported/non-targets.
var unsupportedPlatformClaims = map[string][]*regexp.Regexp{
	"README.md": compileClaims(
		`(?i)linux/macOS`,
		`(?i)macOS has real`,
		`(?i)install script \(Linux, macOS\)`,
		`(?i)sandbox — Windows`,
		`(?i)macOS \(`+"`"+`sandbox-exec`+"`"+`\) are shipped`,
		`(?i)Windows runs unsandboxed`,
		`(?i)\*\*macOS\*\*\s+—`,
		`(?i)\*\*Windows\*\*\s+—`,
		`(?i)on macOS,\s*`+"`"+`net:`,
		`runner_darwin`,
	),
	"docs/security/threatmodel.md": compileClaims(
		`(?i)on macOS it is sandbox-exec`,
		`(?is)sandboxing is platform-dependent.*?macOS\s+sandbox-exec`,
		`(?i)Windows has no native confinement runner yet`,
		`(?i)on Windows, or hosts without`,
	),
	"docs/features/sandboxing.md": compileClaims(
		`(?i)on macOS the deny-default`,
		`(?m)^\| macOS \|`,
		`(?m)^\| Windows \|`,
		`(?i)Windows v2 is deferred`,
		`(?i)macOS uses sandbox-exec`,
	),
	"docs/commands/run.md": compileClaims(
		`(?is)by default on Linux; macOS\s+gets the ceiling-runner`,
		`(?i)Windows v2 sandboxing is\s+still deferred`,
	),
	"docs/plugins/host-imports.md": compileClaims(
		`(?i)macOS supports\s+this without sysctl`,
	),
}

// Non-Linux release references are frozen migration debt. Counting both
// executable configuration and its explanatory comments makes stale release
// documentation visible too. Removal passes without an allowlist edit; any
// new Darwin/Windows target or promise fails. The destination is zero for all
// entries once the Linux-only release files are rewritten.
var nonLinuxReleaseDebt = map[string]map[string]int{
	".goreleaser.yaml":              {"darwin": 0, "windows": 0},
	".github/workflows/release.yml": {"darwin": 0, "windows": 0},
}

var numberedArchitectureCitation = regexp.MustCompile(`(?i)\b(?:DESIGN|PLAN)(?:\.md)?(?s:.{0,100}?)(?:§\s*(?:["“]\s*)?(?:phase\s*)?[0-9]|\bphase\s+[0-9])`)

// Numbered DESIGN/PLAN citations are fragile because those synthesis files
// are intentionally rewritten as the architecture evolves. Existing source
// citations are frozen migration debt; new or edited comments should cite the
// owning EP (and, if useful, a named current heading) instead.
var numberedArchitectureCitationDebt = map[string]int{
	"cmd/stado/plugin_verify.go":               2,
	"cmd/stado/session.go":                     1,
	"cmd/stado/session_fork.go":                2,
	"internal/audit/key.go":                    1,
	"internal/audit/minisign.go":               1,
	"internal/broker/ceiling.go":               1,
	"internal/broker/gitsubagent.go":           1,
	"internal/broker/mount_table_test.go":      1,
	"internal/compact/compact.go":              1,
	"internal/config/config.go":                2,
	"internal/config/paths.go":                 1,
	"internal/fs/fs_test.go":                   1,
	"internal/mcp/capability.go":               1,
	"internal/mcp/client.go":                   1,
	"internal/plugins/crl.go":                  1,
	"internal/plugins/rekor.go":                1,
	"internal/runtime/runtime.go":              1,
	"internal/runtime/session.go":              2,
	"internal/sandbox/landlock_linux.go":       1,
	"internal/sandbox/runner_linux.go":         1,
	"internal/sandbox/seccomp_linux.go":        1,
	"internal/state/git/commit_meta.go":        1,
	"internal/state/git/compaction_markers.go": 1,
	"internal/state/git/session.go":            2,
	"internal/telemetry/metrics.go":            3,
	"internal/telemetry/telemetry.go":          1,
	"internal/telemetry/traceparent.go":        1,
	"internal/tools/executor.go":               1,
	"internal/tools/registry_test.go":          1,
	"internal/tui/model_stream.go":             1,
	"pkg/tool/tool.go":                         1,
}

func TestCurrentDocsDoNotClaimUnsupportedPlatforms(t *testing.T) {
	root := repoRoot(t)
	for path, claims := range unsupportedPlatformClaims {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, claim := range claims {
			if claim.Match(data) {
				t.Errorf("%s contains obsolete macOS/Windows product claim matching %q; stado is Linux-only through v1", path, claim.String())
			}
		}
	}
}

func TestNonLinuxReleaseTargetsCannotGrow(t *testing.T) {
	root := repoRoot(t)
	for path, platforms := range nonLinuxReleaseDebt {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		lower := strings.ToLower(string(data))
		for platform, allowed := range platforms {
			pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(platform) + `\b`)
			count := len(pattern.FindAllStringIndex(lower, -1))
			if count > allowed {
				t.Errorf("%s contains %d %s release reference(s), allowed migration debt is %d; releases are Linux-only through v1", path, count, platform, allowed)
			}
		}
	}
}

// V1 budget authority is token-only. Provider cost remains useful telemetry,
// but reintroducing currency config fields or runtime cap types would make
// provider pricing an enforcement dependency again.
func TestCurrencyBudgetCapsStayRemoved(t *testing.T) {
	root := repoRoot(t)
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`\b(?:WarnUSD|HardUSD|CostCapUSD|ErrCostCapExceeded|budgetWarnUSD|budgetHardUSD)\b`),
		regexp.MustCompile(`(?i)\b(?:warn_usd|hard_usd)\b`),
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" ||
				strings.HasPrefix(rel, "docs/eps") || rel == "evals") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" && ext != ".toml" && ext != ".html" {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// CHANGELOG records the removed pre-v1 surface as history; it is not a
		// source of current configuration or runtime authority.
		if filepath.ToSlash(rel) == "CHANGELOG.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pattern := range forbidden {
			if pattern.Match(data) {
				t.Errorf("%s reintroduces currency budget-cap surface matching %q; keep cost observational and enforcement token-only", filepath.ToSlash(rel), pattern.String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// EP-0064 and the checked-in host-import reference now own the WASM UI ABI.
// Temporary implementation-slice labels such as F9/F10 are not durable
// architecture references and must not creep back into production comments.
func TestPluginUICommentsUseDurableArchitectureReferences(t *testing.T) {
	root := filepath.Join(repoRoot(t), "internal", "plugins", "runtime")
	temporary := regexp.MustCompile(`\bF(?:9(?:a|b(?:\.\d+)?)?|10)\b|\.agent/specs/open/f9b-ui-render`)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if temporary.Match(data) {
			rel, _ := filepath.Rel(repoRoot(t), path)
			t.Errorf("%s cites a temporary UI slice label; cite EP-0064 or docs/plugins/host-imports.md", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// EP-0050 treats the in-process orchestrator as hostile. A host bridge can
// carry a broker-minted opaque binding, but host prose is not authentication
// and an in-process UI gesture is not an unforgeable operator fact.
func TestCurrentDocsDoNotPromoteHostAssertionsToSecurityFacts(t *testing.T) {
	root := repoRoot(t)
	// Use \s rather than a literal space so prose wrapping cannot bypass the
	// boundary check (the drift that prompted this guard was split across two
	// Markdown lines).
	forbidden := regexp.MustCompile(`(?i)\b(?:host[-\s]+authenticated|authenticated\s+facts?|authenticated\s+anchors?|unforgeable\s+facts?)\b`)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || strings.HasPrefix(rel, "docs/eps")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if forbidden.Match(data) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s describes a host assertion as an authenticated/unforgeable fact; name the broker binding, session anchor, or host observation precisely", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNumberedDesignPlanCitationsCannotGrow(t *testing.T) {
	root := repoRoot(t)
	actual := make(map[string]int)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, group := range file.Comments {
			actual[filepath.ToSlash(rel)] += len(numberedArchitectureCitation.FindAllStringIndex(group.Text(), -1))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, count := range actual {
		if count > numberedArchitectureCitationDebt[path] {
			t.Errorf("%s has %d numbered DESIGN/PLAN comment citation(s), allowed debt is %d; cite the owning EP instead", path, count, numberedArchitectureCitationDebt[path])
		}
	}
}

func assertShrinkingDebt(t *testing.T, label string, actual, allowed map[string]int) {
	t.Helper()
	for key, count := range actual {
		if count > allowed[key] {
			t.Errorf("%s %s occurs %d time(s), allowed debt is %d; model-facing application behavior belongs in a WASM plugin", label, key, count, allowed[key])
		}
	}
}

func nativeModelToolSurfaces(t *testing.T, root string) (map[string]int, map[string]int, map[string]int) {
	t.Helper()
	registrations := make(map[string]int)
	definitions := make(map[string]int)
	handlers := make(map[string]int)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		internalTools := importsPath(file, "github.com/foobarto/stado/internal/tools")
		if internalTools || rel == "internal/tui/supervise_tools.go" {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Register" || len(call.Args) != 1 || pluginBackedRegistration(rel, call.Args[0]) {
					return true
				}
				key := rel + "::" + enclosingFunction(file, call.Pos()) + "::Register"
				registrations[key]++
				return true
			})
		}

		agentAliases := importAliases(file, "github.com/foobarto/stado/pkg/agent")
		if len(agentAliases) > 0 {
			constants := stringConstants(file)
			ast.Inspect(file, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if isAgentToolDefType(literal.Type, agentAliases) {
					if name := toolDefName(literal, constants); name != "" {
						key := rel + "::" + enclosingFunction(file, literal.Pos()) + "::" + name
						definitions[key]++
					}
					return true
				}
				array, ok := literal.Type.(*ast.ArrayType)
				if !ok || !isAgentToolDefType(array.Elt, agentAliases) {
					return true
				}
				for _, element := range literal.Elts {
					item, ok := element.(*ast.CompositeLit)
					if !ok {
						continue
					}
					if name := toolDefName(item, constants); name != "" {
						key := rel + "::" + enclosingFunction(file, item.Pos()) + "::" + name
						definitions[key]++
					}
				}
				return true
			})
		}

		if strings.HasPrefix(rel, "internal/tui/handler") {
			ast.Inspect(file, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.BasicLit:
					if value.Kind != token.STRING {
						return true
					}
					text, err := strconv.Unquote(value.Value)
					if err == nil && strings.Contains(text, "__") {
						handlers[rel+"::"+text]++
					}
				case *ast.Ident:
					if strings.HasPrefix(value.Name, "supervise") && strings.HasSuffix(value.Name, "Tool") {
						handlers[rel+"::<identifier:"+value.Name+">"]++
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return registrations, definitions, handlers
}

func pluginBackedRegistration(path string, arg ast.Expr) bool {
	call, ok := arg.(*ast.CallExpr)
	if ok {
		if fn, ok := call.Fun.(*ast.Ident); ok {
			switch fn.Name {
			case "newBundledPluginTool", "newBundledWasmTool", "newInstalledPluginTool", "newWasmMigrationTool":
				return true
			}
		}
	}
	ident, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	if ident.Name == "pt" && path == "internal/runtime/plugin_overrides.go" {
		return true
	}
	// These exact loops register PluginTool values returned by the verified,
	// manifest-bound persistent WASM application loader. They are the desired
	// plugin dispatch surface, not a native implementation hidden behind a Go
	// tool name. Keep the exemption path- and variable-specific so an unrelated
	// registration in either composition root is still reported.
	return ident.Name == "applicationTool" &&
		(path == "internal/runtime/lifecycle_apps.go" || path == "internal/tui/model_plugins.go")
}

func importsPath(file *ast.File, want string) bool {
	return len(importAliases(file, want)) > 0
}

func importAliases(file *ast.File, want string) map[string]bool {
	aliases := make(map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != want {
			continue
		}
		name := filepath.Base(want)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}

func stringConstants(file *ast.File) map[string]string {
	constants := make(map[string]string)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, raw := range gen.Specs {
			spec, ok := raw.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range spec.Names {
				if i >= len(spec.Values) {
					continue
				}
				literal, ok := spec.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				if value, err := strconv.Unquote(literal.Value); err == nil {
					constants[name.Name] = value
				}
			}
		}
	}
	return constants
}

func isAgentToolDefType(expr ast.Expr, aliases map[string]bool) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "ToolDef" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && aliases[pkg.Name]
}

func toolDefName(literal *ast.CompositeLit, constants map[string]string) string {
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		switch value := field.Value.(type) {
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				name, _ := strconv.Unquote(value.Value)
				return name
			}
		case *ast.Ident:
			return constants[value.Name]
		}
	}
	return ""
}

func directWALCalls(t *testing.T, root string) map[string]int {
	t.Helper()
	actual := make(map[string]int)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/broker/") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := walAliases(file)
		if len(aliases) == 0 {
			return nil
		}
		if aliases["."] {
			actual[rel+"::<imports>::dot"]++
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Open" && selector.Sel.Name != "OpenShared") {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || !aliases[pkg.Name] {
				return true
			}
			key := rel + "::" + enclosingFunction(file, call.Pos()) + "::" + selector.Sel.Name
			actual[key]++
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return actual
}

func importedFunctionCalls(t *testing.T, root, wantImport string, names map[string]bool) map[string]int {
	t.Helper()
	actual := make(map[string]int)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := importAliases(file, wantImport)
		if len(aliases) == 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !names[selector.Sel.Name] {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || !aliases[pkg.Name] {
				return true
			}
			key := rel + "::" + enclosingFunction(file, call.Pos()) + "::" + selector.Sel.Name
			actual[key]++
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return actual
}

func walAliases(file *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != walImportPath {
			continue
		}
		name := "wal"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" {
			aliases[name] = true
		}
	}
	return aliases
}

func enclosingFunction(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Body != nil && fn.Body.Pos() <= pos && pos <= fn.Body.End() {
			return fn.Name.Name
		}
	}
	return "<package>"
}

func compileClaims(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		out = append(out, regexp.MustCompile(pattern))
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return root
}
