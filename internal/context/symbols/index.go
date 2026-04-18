package symbols

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Symbol struct {
	Name     string
	Kind     string // func, type, const, var, class, def
	File     string
	Line     int
	Language string
}

type SymbolIndex struct {
	mu      sync.RWMutex
	symbols map[string][]Symbol
	workdir string
}

var langPatterns = map[string][]*regexp.Regexp{
	"go": {
		regexp.MustCompile(`^func\s+(?:\([^)]+\)\s+)?(\w+)\s*\(`),
		regexp.MustCompile(`^type\s+(\w+)\s+(?:struct|interface|func|map|chan|slice|\*?\w+)`),
		regexp.MustCompile(`^const\s+\(([^)]*)\)`),
		regexp.MustCompile(`^var\s+\(([^)]*)\)`),
	},
	"python": {
		regexp.MustCompile(`^(?:async\s+)?def\s+(\w+)\s*\(`),
		regexp.MustCompile(`^class\s+(\w+)`),
	},
	"javascript": {
		regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\(`),
		regexp.MustCompile(`^(?:export\s+)?class\s+(\w+)`),
		regexp.MustCompile(`^(?:export\s+)?const\s+(\w+)\s*=\s*(?:function|async\s+function|\()`),
	},
	"typescript": {
		regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*[<\(]`),
		regexp.MustCompile(`^(?:export\s+)?(?:abstract\s+)?class\s+(\w+)`),
		regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*:\s*(?:function|<)`),
		regexp.MustCompile(`^(?:export\s+)?interface\s+(\w+)`),
		regexp.MustCompile(`^(?:export\s+)?type\s+(\w+)\s*=`),
	},
	"rust": {
		regexp.MustCompile(`^(?:pub\s+)?(?:async\s+)?fn\s+(\w+)\s*[<\(]`),
		regexp.MustCompile(`^(?:pub\s+)?struct\s+(\w+)`),
		regexp.MustCompile(`^(?:pub\s+)?enum\s+(\w+)`),
		regexp.MustCompile(`^(?:pub\s+)?trait\s+(\w+)`),
	},
}

var extToLang = map[string]string{
	".go":  "go",
	".py":  "python",
	".js":  "javascript",
	".jsx": "javascript",
	".ts":  "typescript",
	".tsx": "typescript",
	".rs":  "rust",
}

func NewSymbolIndex(workdir string) *SymbolIndex {
	return &SymbolIndex{
		symbols: make(map[string][]Symbol),
		workdir: workdir,
	}
}

func (si *SymbolIndex) IndexFile(path string) error {
	fullPath := filepath.Join(si.workdir, path)
	ext := filepath.Ext(path)
	lang, ok := extToLang[ext]
	if !ok {
		return nil
	}

	patterns, ok := langPatterns[lang]
	if !ok {
		return nil
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var symbols []Symbol
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		
		for _, pat := range patterns {
			if matches := pat.FindStringSubmatch(line); len(matches) > 1 {
				kind := guessKind(pat.String())
				symbols = append(symbols, Symbol{
					Name:     matches[1],
					Kind:     kind,
					File:     path,
					Line:     lineNum,
					Language: lang,
				})
			}
		}
	}

	si.mu.Lock()
	si.symbols[path] = symbols
	si.mu.Unlock()

	return nil
}

func (si *SymbolIndex) DeleteFile(path string) {
	si.mu.Lock()
	delete(si.symbols, path)
	si.mu.Unlock()
}

func (si *SymbolIndex) Search(name string) []Symbol {
	si.mu.RLock()
	defer si.mu.RUnlock()

	var results []Symbol
	for _, syms := range si.symbols {
		for _, s := range syms {
			if strings.Contains(strings.ToLower(s.Name), strings.ToLower(name)) {
				results = append(results, s)
			}
		}
	}
	return results
}

func (si *SymbolIndex) All() []Symbol {
	si.mu.RLock()
	defer si.mu.RUnlock()

	var all []Symbol
	for _, syms := range si.symbols {
		all = append(all, syms...)
	}
	return all
}

func guessKind(pattern string) string {
	switch {
	case strings.Contains(pattern, "func") || strings.Contains(pattern, "fn") || strings.Contains(pattern, "def"):
		return "func"
	case strings.Contains(pattern, "type") || strings.Contains(pattern, "struct") || strings.Contains(pattern, "class"):
		return "type"
	case strings.Contains(pattern, "const"):
		return "const"
	case strings.Contains(pattern, "var") || strings.Contains(pattern, "let"):
		return "var"
	case strings.Contains(pattern, "interface"):
		return "interface"
	case strings.Contains(pattern, "enum"):
		return "enum"
	case strings.Contains(pattern, "trait"):
		return "trait"
	default:
		return "symbol"
	}
}
