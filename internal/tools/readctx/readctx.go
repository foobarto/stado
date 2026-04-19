// Package readctx implements the read_with_context tool — read a file AND
// the files it directly imports, so the model gets enough context without
// chaining multiple read calls.
//
// v1: Go-native import resolution via go/parser (no LSP dependency). Other
// languages fall back to plain file-read semantics until Phase 4.3's LSP
// client lands; at that point document_symbols / workspace_symbols can
// resolve imports cross-language.
package readctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/pkg/tool"
)

type Tool struct{}

func (Tool) Name() string { return "read_with_context" }
func (Tool) Description() string {
	return "Read a file plus its direct imports/dependencies. Goes one hop deep."
}

func (Tool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path from workdir",
			},
			"max_bytes_per_file": map[string]any{
				"type":        "integer",
				"description": "Truncate each included file at this size (default 64k)",
			},
		},
		"required": []string{"path"},
	}
}

// Class: read-only.
func (Tool) Class() tool.Class { return tool.ClassNonMutating }

type Args struct {
	Path            string `json:"path"`
	MaxBytesPerFile int    `json:"max_bytes_per_file"`
}

func (t Tool) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var a Args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
	}
	if a.Path == "" {
		return tool.Result{Error: "path required"}, errors.New("readctx: path required")
	}
	if a.MaxBytesPerFile <= 0 {
		a.MaxBytesPerFile = 64 * 1024
	}

	target := filepath.Join(h.Workdir(), a.Path)
	info, err := os.Stat(target)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if info.IsDir() {
		return tool.Result{Error: "path is a directory"}, fmt.Errorf("readctx: %s is a directory", a.Path)
	}

	pairs, err := gather(target, h.Workdir(), a.MaxBytesPerFile)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: format(pairs)}, nil
}

type filePair struct {
	rel  string
	body string
}

// gather reads the target file and any directly-imported files (Go-aware).
// Other languages return just the target.
func gather(target, workdir string, maxBytes int) ([]filePair, error) {
	data, err := readBounded(target, maxBytes)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(workdir, target)
	out := []filePair{{rel: rel, body: data}}

	if filepath.Ext(target) == ".go" {
		imports, err := resolveGoImports(target, workdir, maxBytes)
		if err == nil {
			out = append(out, imports...)
		}
	}
	return out, nil
}

// resolveGoImports parses the Go file for `import` statements and walks each
// imported package's module directory to read up to one representative file
// per package. Limits: in-repo packages only (no GOPATH/module cache reads).
func resolveGoImports(filePath, workdir string, maxBytes int) ([]filePair, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []filePair

	// Locate the module root: walk up from filePath until a go.mod is found.
	modRoot, modPath := findModuleRoot(filepath.Dir(filePath))

	for _, imp := range af.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if seen[path] {
			continue
		}
		seen[path] = true

		// Only resolve in-repo imports.
		if modPath == "" || !strings.HasPrefix(path, modPath) {
			continue
		}
		rel := strings.TrimPrefix(path, modPath)
		rel = strings.TrimPrefix(rel, "/")
		pkgDir := filepath.Join(modRoot, rel)
		if stat, err := os.Stat(pkgDir); err != nil || !stat.IsDir() {
			continue
		}

		// Pick one representative file per package — prefer <pkgname>.go, else
		// the first non-test .go file.
		entries, _ := os.ReadDir(pkgDir)
		var candidate string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			candidate = filepath.Join(pkgDir, e.Name())
			if strings.HasPrefix(e.Name(), filepath.Base(pkgDir)+".") {
				break // prefer <pkgname>.go
			}
		}
		if candidate == "" {
			continue
		}
		body, err := readBounded(candidate, maxBytes)
		if err != nil {
			continue
		}
		relp, _ := filepath.Rel(workdir, candidate)
		out = append(out, filePair{rel: relp, body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// findModuleRoot walks up from dir until it sees a go.mod. Returns (root
// dir, module path) or empty strings when none found.
func findModuleRoot(dir string) (string, string) {
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "module ") {
					return dir, strings.TrimSpace(strings.TrimPrefix(line, "module"))
				}
			}
			return dir, ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func readBounded(path string, max int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > max {
		return string(data[:max]) + "\n…[truncated]", nil
	}
	return string(data), nil
}

func format(pairs []filePair) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "=== %s ===\n%s\n", p.rel, p.body)
	}
	return b.String()
}
