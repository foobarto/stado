package tui

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/tui/filepicker"
	"github.com/foobarto/stado/internal/workdirpath"
)

type symbolCandidate struct {
	Name string
	Kind string
	Path string
	Line int
}

const maxSymbolSourceFileBytes int64 = 1 << 20

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
		if d.Type()&os.ModeSymlink != 0 {
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
		case ".js", ".jsx", ".ts", ".tsx":
			out = append(out, scanScriptFileSymbols(rel, path, limit-len(out))...)
		case ".sh", ".bash":
			out = append(out, scanShellFileSymbols(rel, path, limit-len(out))...)
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
	data, err := workdirpath.ReadRegularFileNoSymlinkLimited(path, maxSymbolSourceFileBytes)
	if err != nil {
		return nil
	}
	file, parseErr := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
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
	data, err := workdirpath.ReadRegularFileNoSymlinkLimited(path, maxSymbolSourceFileBytes)
	if err != nil {
		return nil
	}

	var out []symbolCandidate
	scanner := bufio.NewScanner(bytes.NewReader(data))
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

func scanScriptFileSymbols(rel, path string, limit int) []symbolCandidate {
	if limit <= 0 {
		return nil
	}
	data, err := workdirpath.ReadRegularFileNoSymlinkLimited(path, maxSymbolSourceFileBytes)
	if err != nil {
		return nil
	}

	lang := "js"
	switch filepath.Ext(path) {
	case ".ts", ".tsx":
		lang = "ts"
	}
	var out []symbolCandidate
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Text()
		if strings.TrimLeft(raw, " \t") != raw {
			continue
		}
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "//") || strings.HasPrefix(text, "import ") {
			continue
		}
		text = stripScriptDeclarationPrefix(text)
		if name, ok := scriptSymbolName(text, "class "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: lang + " class", Path: rel, Line: line})
		} else if name, ok := scriptSymbolName(text, "function "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: lang + " func", Path: rel, Line: line})
		} else if name, ok := scriptSymbolName(text, "const "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: lang + " const", Path: rel, Line: line})
		} else if name, ok := scriptSymbolName(text, "let "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: lang + " let", Path: rel, Line: line})
		} else if name, ok := scriptSymbolName(text, "var "); ok {
			out = append(out, symbolCandidate{Name: name, Kind: lang + " var", Path: rel, Line: line})
		}
		if len(out) >= limit {
			return out[:limit]
		}
	}
	return out
}

func stripScriptDeclarationPrefix(line string) string {
	for {
		switch {
		case strings.HasPrefix(line, "export default "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "export default "))
		case strings.HasPrefix(line, "export "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		case strings.HasPrefix(line, "declare "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "declare "))
		case strings.HasPrefix(line, "async function "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "async "))
		default:
			return line
		}
	}
}

func scriptSymbolName(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if rest == "" {
		return "", false
	}
	end := 0
	for end < len(rest) {
		if isScriptIdentChar(rest[end], end) {
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

func isScriptIdentChar(ch byte, pos int) bool {
	if ch == '_' || ch == '$' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' {
		return true
	}
	return pos > 0 && ch >= '0' && ch <= '9'
}

func scanShellFileSymbols(rel, path string, limit int) []symbolCandidate {
	if limit <= 0 {
		return nil
	}
	data, err := workdirpath.ReadRegularFileNoSymlinkLimited(path, maxSymbolSourceFileBytes)
	if err != nil {
		return nil
	}

	var out []symbolCandidate
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Text()
		if strings.TrimLeft(raw, " \t") != raw {
			continue
		}
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if name, ok := shellFunctionName(text); ok {
			out = append(out, symbolCandidate{Name: name, Kind: "shell func", Path: rel, Line: line})
		}
		if len(out) >= limit {
			return out[:limit]
		}
	}
	return out
}

func shellFunctionName(line string) (string, bool) {
	if strings.HasPrefix(line, "function ") {
		rest := strings.TrimSpace(strings.TrimPrefix(line, "function "))
		name, end := readShellName(rest)
		if name == "" {
			return "", false
		}
		tail := strings.TrimSpace(rest[end:])
		if tail == "" || strings.HasPrefix(tail, "{") || strings.HasPrefix(tail, "(") {
			return name, true
		}
		return "", false
	}

	name, end := readShellName(line)
	if name == "" {
		return "", false
	}
	tail := strings.TrimSpace(line[end:])
	if strings.HasPrefix(tail, "()") {
		return name, true
	}
	return "", false
}

func readShellName(line string) (string, int) {
	end := 0
	for end < len(line) {
		if isShellNameChar(line[end], end) {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", 0
	}
	return line[:end], end
}

func isShellNameChar(ch byte, pos int) bool {
	if ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' {
		return true
	}
	return pos > 0 && (ch == '-' || ch >= '0' && ch <= '9')
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
