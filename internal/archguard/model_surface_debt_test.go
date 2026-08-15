package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// migrationScaffoldingDebt is the exact remaining native-to-WASM fallback
// surface. It is not a compatibility promise: every entry is expected to
// disappear once bundled manifests are the only source of model-tool
// contracts. Missing entries are therefore allowed; additions are not.
var migrationScaffoldingDebt = map[string]int{}

// nativeGuidanceDebt covers both the Go-owned policy package and every prompt
// composition seam that injects its result into a model turn. Host-derived,
// bounded facts may remain native; choosing wording, thresholds and actions is
// application policy and must move to a signed lifecycle application.
var nativeGuidanceDebt = map[string]int{}

// duplicatedToolMetadataDebt inventories the Go table which duplicates
// manifest-owned names/categories and its production consumers. The target is
// zero: verified manifests become the sole source of model-visible contract
// metadata.
var duplicatedToolMetadataDebt = map[string]int{}

func TestNativeWASMMigrationScaffoldingCannotGrow(t *testing.T) {
	actual := migrationScaffolding(t, repoRoot(t))
	assertShrinkingDebt(t, "native WASM migration scaffolding", actual, migrationScaffoldingDebt)
}

func TestNativeGuidancePolicyCannotGrow(t *testing.T) {
	actual := nativeGuidanceSurfaces(t, repoRoot(t))
	assertShrinkingDebt(t, "native guidance policy", actual, nativeGuidanceDebt)
}

func TestDuplicatedToolMetadataCannotGrow(t *testing.T) {
	actual := duplicatedToolMetadataSurfaces(t, repoRoot(t))
	assertShrinkingDebt(t, "duplicated Go tool metadata", actual, duplicatedToolMetadataDebt)
}

func migrationScaffolding(t *testing.T, root string) map[string]int {
	t.Helper()
	targets := map[string]bool{
		"wasmFamily":           true,
		"wasmTool":             true,
		"wasmFamilies":         true,
		"ApplyWasmMigration":   true,
		"wasmMigrationTool":    true,
		"newWasmMigrationTool": true,
		"UseWasm":              true,
	}
	actual := make(map[string]int)
	walkProductionGo(t, root, func(rel string, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				if targets[value.Name] {
					actual[rel+"::<identifier:"+value.Name+">"]++
				}
			case *ast.BasicLit:
				if value.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(value.Value)
				if err == nil && strings.Contains(text, "use_wasm") {
					actual[rel+"::<literal:use_wasm>"]++
				}
			}
			return true
		})
	})
	return actual
}

func nativeGuidanceSurfaces(t *testing.T, root string) map[string]int {
	t.Helper()
	actual := make(map[string]int)
	walkProductionGo(t, root, func(rel string, file *ast.File) {
		if strings.HasPrefix(rel, "internal/guidance/") {
			for _, decl := range file.Decls {
				switch value := decl.(type) {
				case *ast.FuncDecl:
					actual[rel+"::<declaration:func:"+value.Name.Name+">"]++
				case *ast.GenDecl:
					if value.Tok == token.IMPORT {
						continue
					}
					for _, raw := range value.Specs {
						switch spec := raw.(type) {
						case *ast.TypeSpec:
							actual[rel+"::<declaration:type:"+spec.Name.Name+">"]++
						case *ast.ValueSpec:
							for _, name := range spec.Names {
								actual[rel+"::<declaration:"+strings.ToLower(value.Tok.String())+":"+name.Name+">"]++
							}
						}
					}
				}
			}
		}

		guidanceAliases := importAliases(file, "github.com/foobarto/stado/internal/guidance")
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if ok && guidanceAliases[pkg.Name] {
					key := rel + "::" + enclosingFunction(file, value.Pos()) + "::guidance." + selector.Sel.Name
					actual[key]++
				}
			case *ast.Ident:
				// A future Go policy is more likely to choose a new helper name
				// than resurrect the deleted package. Keep any production symbol
				// naming itself guidance behind an explicit architecture review;
				// bounded host fact/contribution primitives deliberately use
				// factual names instead (EP-0060/0066).
				if strings.Contains(strings.ToLower(value.Name), "guidance") {
					key := rel + "::" + enclosingFunction(file, value.Pos()) + "::<identifier:" + value.Name + ">"
					actual[key]++
				}
			case *ast.BasicLit:
				if value.Kind == token.STRING {
					text, err := strconv.Unquote(value.Value)
					if err == nil && strings.Contains(text, "Stado workflow guidance") {
						actual[rel+"::"+enclosingFunction(file, value.Pos())+"::<native-guidance-template>"]++
					}
				}
			}
			return true
		})
	})
	return actual
}

func duplicatedToolMetadataSurfaces(t *testing.T, root string) map[string]int {
	t.Helper()
	actual := make(map[string]int)
	walkProductionGo(t, root, func(rel string, file *ast.File) {
		if rel == "internal/runtime/tool_metadata.go" {
			for _, decl := range file.Decls {
				general, ok := decl.(*ast.GenDecl)
				if !ok || general.Tok != token.VAR {
					continue
				}
				for _, raw := range general.Specs {
					spec, ok := raw.(*ast.ValueSpec)
					if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
						continue
					}
					switch spec.Names[0].Name {
					case "canonicalToolMetadata", "legacyBareAliases", "hiddenLegacyTools":
						literal, ok := spec.Values[0].(*ast.CompositeLit)
						if ok {
							actual[rel+"::<map:"+spec.Names[0].Name+">"] += len(literal.Elts)
						}
					}
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if name == "LookupToolMetadata" {
				key := rel + "::" + enclosingFunction(file, call.Pos()) + "::LookupToolMetadata"
				actual[key]++
			}
			return true
		})
	})
	return actual
}

func walkProductionGo(t *testing.T, root string, visit func(rel string, file *ast.File)) {
	t.Helper()
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
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(rel), file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeInterceptedToolNameCoverage(t *testing.T) {
	for _, name := range []string{"tools__describe", "skills__load", "agent__spawn", "supervise__complete"} {
		if !nativeInterceptedToolName(name) {
			t.Errorf("expected %q to be guarded", name)
		}
	}
	for _, name := range []string{"fs__read", "ordinary text"} {
		if nativeInterceptedToolName(name) {
			t.Errorf("did not expect %q to be classified as native result interception debt", name)
		}
	}
}

func TestNativeRegistrationExpressionClassification(t *testing.T) {
	cases := map[string]string{
		"&metaSearch{}":        "metaSearch",
		"&bundledPluginTool{}": "bundledPluginTool",
	}
	for source, want := range cases {
		expr, err := parser.ParseExpr(source)
		if err != nil {
			t.Fatalf("parse %q: %v", source, err)
		}
		if got := registrationExpressionName(expr); got != want {
			t.Errorf("registrationExpressionName(%s) = %q, want %q", source, got, want)
		}
	}
}
