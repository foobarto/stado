// Package astgrep wraps the ast-grep CLI for structural code queries.
//
// Same pattern as internal/tools/rg: v1 requires `ast-grep` on PATH (or
// ${STADO_AST_GREP}); embed path lands with the ripgrep embed in a follow-up.
package astgrep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/limitedio"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

const (
	maxASTGrepOutputBytes = 1 << 20
	maxASTGrepErrorBytes  = 64 << 10
)

type Tool struct {
	Binary string
}

func (Tool) Name() string { return "ast_grep" }
func (Tool) Description() string {
	return "Structural code search and rewrite via ast-grep (tree-sitter patterns)."
}

func (Tool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":  map[string]any{"type": "string", "description": "ast-grep pattern, e.g. 'fmt.Println($X)'"},
			"language": map[string]any{"type": "string", "description": "Language hint (go, python, typescript, rust, …)"},
			"path":     map[string]any{"type": "string", "description": "Relative to workdir"},
			"rewrite":  map[string]any{"type": "string", "description": "Optional: replacement pattern for in-place rewrite"},
		},
		"required": []string{"pattern"},
	}
}

// ast-grep can rewrite files in place when `rewrite` is set, so it must be
// classified as exec-class to preserve tree commits whenever it mutates.
func (Tool) Class() tool.Class { return tool.ClassExec }

type Args struct {
	Pattern  string `json:"pattern"`
	Language string `json:"language"`
	Path     string `json:"path"`
	Rewrite  string `json:"rewrite"`
}

func (t Tool) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var a Args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
	}
	if a.Pattern == "" {
		return tool.Result{Error: "pattern required"}, errors.New("ast_grep: pattern required")
	}
	bin, err := ResolveBinary(t.Binary)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	searchPath, err := workdirpath.Resolve(h.Workdir(), ".", false)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if a.Path != "" {
		searchPath, err = workdirpath.Resolve(h.Workdir(), a.Path, false)
		if err != nil {
			return tool.Result{Error: err.Error()}, err
		}
	}

	args := []string{"run", "--json", "--pattern", a.Pattern}
	if a.Language != "" {
		args = append(args, "--lang", a.Language)
	}
	if a.Rewrite != "" {
		args = append(args, "--rewrite", a.Rewrite, "--update-all")
	}
	args = append(args, searchPath)

	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- trusted ast-grep binary with fixed argument vector, no shell.
	stdout := limitedio.NewBuffer(maxASTGrepOutputBytes)
	stderr := limitedio.NewBuffer(maxASTGrepErrorBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); ok {
			// Many ast-grep exit codes are non-zero for "no matches" — treat
			// empty stdout as no-match rather than a failure.
			if stdout.Len() == 0 {
				return tool.Result{Content: "No matches found"}, nil
			}
		} else {
			return tool.Result{Error: runErr.Error()}, runErr
		}
	}
	if stdout.Truncated() {
		err := fmt.Errorf("ast_grep output exceeds %d bytes", maxASTGrepOutputBytes)
		return tool.Result{Error: err.Error()}, err
	}

	matches, err := parseMatches(stdout.Bytes(), h.Workdir())
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(matches) == 0 {
		return tool.Result{Content: strings.TrimSpace(astGrepOutputString(stderr, "ast_grep stderr", maxASTGrepErrorBytes))}, nil
	}
	return tool.Result{Content: strings.Join(matches, "\n")}, nil
}

func astGrepOutputString(buf *limitedio.Buffer, label string, maxBytes int) string {
	s := buf.String()
	if buf.Truncated() {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += fmt.Sprintf("[truncated: %s exceeded %d bytes]\n", label, maxBytes)
	}
	return s
}

// ResolveBinary picks an ast-grep binary. Precedence:
//  1. explicit override arg
//  2. ${STADO_AST_GREP} env var
//  3. bundled blob (release builds include one; extracted on first use)
//  4. PATH lookup (accepts `ast-grep` or older alias `sg`)
func ResolveBinary(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if v := os.Getenv("STADO_AST_GREP"); v != "" {
		return v, nil
	}
	if path, err := bundledBinary(); err == nil {
		return path, nil
	}
	for _, name := range []string{"ast-grep", "sg"} {
		if full, err := exec.LookPath(name); err == nil {
			return full, nil
		}
	}
	return "", fmt.Errorf("ast-grep not found on PATH. Install:\n" +
		"  brew install ast-grep · cargo install ast-grep --locked · npm i -g @ast-grep/cli\n" +
		"Or set STADO_AST_GREP=/path/to/ast-grep")
}

// parseMatches reads ast-grep's JSON array output and formats
// `rel/path:line:range:match` lines.
func parseMatches(raw []byte, workdir string) ([]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var matches []struct {
		File  string `json:"file"`
		Range struct {
			Start struct{ Line int } `json:"start"`
			End   struct{ Line int } `json:"end"`
		} `json:"range"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &matches); err != nil {
		return nil, fmt.Errorf("ast_grep: parse json: %w", err)
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, _ := filepath.Rel(workdir, m.File)
		snippet := strings.ReplaceAll(m.Text, "\n", " / ")
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		out = append(out, fmt.Sprintf("%s:%d-%d:%s", rel, m.Range.Start.Line, m.Range.End.Line, snippet))
	}
	return out, nil
}
