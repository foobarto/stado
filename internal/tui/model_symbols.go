package tui

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/tui/filepicker"
)

type symbolCandidate struct {
	Name string
	Kind string
	Path string
	Line int
}

func (m *Model) filePickerSymbolItems() []filepicker.Item {
	root := m.sidebarRepoRoot()
	if root == "" {
		root = m.cwd
	}
	if root == "" {
		return nil
	}
	symbols := scanSymbols(root)
	out := make([]filepicker.Item, 0, len(symbols))
	for _, sym := range symbols {
		loc := fmt.Sprintf("%s:%d", sym.Path, sym.Line)
		out = append(out, filepicker.Item{
			Kind:    filepicker.KindSymbol,
			ID:      sym.Name + " " + sym.Kind + " " + loc,
			Display: sym.Name,
			Meta:    sym.Kind + "  " + loc,
			Insert:  loc,
		})
	}
	return out
}

func scanSymbols(root string) []symbolCandidate {
	const limit = 300
	fset := token.NewFileSet()
	var out []symbolCandidate
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if skipSymbolDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= limit {
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch filepath.Ext(path) {
		case ".go":
			out = append(out, scanGoFileSymbols(fset, rel, path, limit-len(out))...)
		case ".py":
			out = append(out, scanPythonFileSymbols(rel, path, limit-len(out))...)
		default:
			return nil
		}
		if len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func scanGoFileSymbols(fset *token.FileSet, rel, path string, limit int) []symbolCandidate {
	if limit <= 0 {
		return nil
	}
	file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if parseErr != nil {
		return nil
	}
	var out []symbolCandidate
	for _, decl := range file.Decls {
		out = append(out, declarationSymbols(fset, rel, decl)...)
		if len(out) >= limit {
			return out[:limit]
		}
	}
	return out
}

func declarationSymbols(fset *token.FileSet, rel string, decl ast.Decl) []symbolCandidate {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		name := d.Name.Name
		kind := "func"
		if d.Recv != nil && len(d.Recv.List) > 0 {
			kind = "method"
			if recv := receiverName(d.Recv.List[0].Type); recv != "" {
				name = recv + "." + name
			}
		}
		return []symbolCandidate{{
			Name: name,
			Kind: kind,
			Path: rel,
			Line: fset.Position(d.Name.Pos()).Line,
		}}
	case *ast.GenDecl:
		out := []symbolCandidate{}
		kind := strings.ToLower(d.Tok.String())
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				out = append(out, symbolCandidate{
					Name: s.Name.Name,
					Kind: "type",
					Path: rel,
					Line: fset.Position(s.Name.Pos()).Line,
				})
			case *ast.ValueSpec:
				for _, name := range s.Names {
					out = append(out, symbolCandidate{
						Name: name.Name,
						Kind: kind,
						Path: rel,
						Line: fset.Position(name.Pos()).Line,
					})
				}
			}
		}
		return out
	default:
		return nil
	}
}

func scanPythonFileSymbols(rel, path string, limit int) []symbolCandidate {
	if limit <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []symbolCandidate
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		trimmed := strings.TrimLeft(text, " \t")
		if trimmed != text {
			continue
		}
		if name, ok := pythonSymbolName(trimmed, "class "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: "python class", Path: rel, Line: line})
		} else if name, ok := pythonSymbolName(trimmed, "def "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: "python func", Path: rel, Line: line})
		} else if name, ok := pythonSymbolName(trimmed, "async def "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: "python func", Path: rel, Line: line})
		}
		if len(out) >= limit {
			return out[:limit]
		}
	}
	return out
}

func pythonSymbolName(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if rest == "" {
		return "", false
	}
	end := 0
	for end < len(rest) {
		if isPythonIdentChar(rest[end], end) {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", false
	}
	return rest[:end], true
}

func isPythonIdentChar(ch byte, pos int) bool {
	if ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' {
		return true
	}
	return pos > 0 && ch >= '0' && ch <= '9'
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	default:
		return ""
	}
}

func skipSymbolDir(name string) bool {
	return strings.HasPrefix(name, ".") ||
		name == "node_modules" ||
		name == "vendor" ||
		name == "__pycache__" ||
		name == ".venv" ||
		name == "venv" ||
		name == "dist" ||
		name == "build" ||
		name == "target"
}
