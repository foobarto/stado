// Package rg is a ripgrep-backed code search tool for stado.
//
// v1 requires the `rg` binary on PATH (or ${STADO_RG} for an explicit path)
// and fails with an install hint when missing. PLAN §4.1's embed path —
// shipping per-OS/arch ripgrep release assets via go:embed, extracting to
// ${XDG_CACHE_HOME}/stado/bin/rg on first use with sha256 verification —
// lands in a follow-up; the build-time download pipeline is what's
// substantial there, not this tool surface.
package rg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

// Tool is the ripgrep code-search tool exposed to models.
type Tool struct {
	// Binary overrides the `rg` lookup — useful for tests or pinning a
	// specific version. Empty = discover on PATH (or ${STADO_RG}).
	Binary string
}

func (Tool) Name() string        { return "ripgrep" }
func (Tool) Description() string {
	return "Fast file-contents search via ripgrep. Structural, ignores .gitignore by default."
}

func (Tool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex pattern (ripgrep-compatible)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Relative to workdir. Default: workdir root.",
			},
			"globs": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Include-filter globs (e.g. '*.go', '!*_test.go').",
			},
			"case_sensitive": map[string]any{
				"type":        "boolean",
				"description": "Default: auto (case-smart matching).",
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Lines of context around each match. Default 0.",
			},
			"max_matches": map[string]any{
				"type":        "integer",
				"description": "Cap on total matches returned. Default 100 (DESIGN §Tool-output curation).",
			},
			"include_hidden": map[string]any{
				"type":        "boolean",
				"description": "Include dotfiles. Default false.",
			},
		},
		"required": []string{"pattern"},
	}
}

// Class — read-only search, no worktree mutations.
func (Tool) Class() tool.Class { return tool.ClassNonMutating }

type Args struct {
	Pattern       string   `json:"pattern"`
	Path          string   `json:"path"`
	Globs         []string `json:"globs"`
	CaseSensitive *bool    `json:"case_sensitive"`
	Context       int      `json:"context"`
	MaxMatches    int      `json:"max_matches"`
	IncludeHidden bool     `json:"include_hidden"`
}

func (t Tool) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var a Args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
	}
	if a.Pattern == "" {
		return tool.Result{Error: "pattern required"}, errors.New("rg: pattern required")
	}

	bin, err := ResolveBinary(t.Binary)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	searchPath := filepath.Join(h.Workdir(), a.Path)
	if a.Path == "" {
		searchPath = h.Workdir()
	}
	if a.MaxMatches <= 0 {
		a.MaxMatches = budget.RipgrepMatches
	}

	args := []string{"--json", "--line-number"}
	if a.Context > 0 {
		args = append(args, "--context", fmt.Sprintf("%d", a.Context))
	}
	if a.CaseSensitive != nil {
		if *a.CaseSensitive {
			args = append(args, "--case-sensitive")
		} else {
			args = append(args, "--ignore-case")
		}
	}
	if a.IncludeHidden {
		args = append(args, "--hidden")
	}
	for _, g := range a.Globs {
		args = append(args, "--glob", g)
	}
	args = append(args, "--max-count", fmt.Sprintf("%d", a.MaxMatches), "--", a.Pattern, searchPath)

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// ripgrep exit codes: 0 = matches, 1 = no matches, 2 = error.
	if ee, ok := runErr.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code == 1 {
			return tool.Result{Content: "No matches found"}, nil
		} else if code == 2 {
			return tool.Result{Error: strings.TrimSpace(stderr.String())}, fmt.Errorf("rg: exit 2: %s", stderr.String())
		}
	} else if runErr != nil {
		return tool.Result{Error: runErr.Error()}, runErr
	}

	matches, err := parseJSON(stdout.Bytes(), h.Workdir(), a.MaxMatches)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(matches) == 0 {
		return tool.Result{Content: "No matches found"}, nil
	}
	// The parse-loop already caps at MaxMatches, but a user who passes
	// `max_matches: 10000` still gets exactly that many — drop a
	// truncation marker when the number of matches equals the cap, as
	// it almost certainly clipped more than it kept.
	joined := strings.Join(matches, "\n")
	if len(matches) >= a.MaxMatches {
		joined += fmt.Sprintf("\n[truncated: capped at %d matches — narrow the pattern or raise max_matches]", a.MaxMatches)
	}
	return tool.Result{Content: joined}, nil
}

// ResolveBinary picks the rg binary to use. Precedence: explicit override,
// ${STADO_RG}, PATH lookup. Returns a friendly error pointing at install
// docs when none found.
func ResolveBinary(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := envRG(); env != "" {
		return env, nil
	}
	full, err := exec.LookPath("rg")
	if err != nil {
		return "", fmt.Errorf("ripgrep not found on PATH. Install:\n" +
			"  apt/dnf install ripgrep · brew install ripgrep · cargo install ripgrep\n" +
			"Or set STADO_RG=/path/to/rg")
	}
	return full, nil
}

// parseJSON consumes ripgrep's `--json` output and formats compact
// `rel/path:line:text` lines. Caps at maxMatches.
func parseJSON(raw []byte, workdir string, maxMatches int) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	type rgMsg struct {
		Type string `json:"type"`
		Data struct {
			Path  struct{ Text string } `json:"path"`
			Lines struct{ Text string } `json:"lines"`
			LineNumber int               `json:"line_number"`
		} `json:"data"`
	}
	var out []string
	for scanner.Scan() {
		var m rgMsg
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			continue
		}
		if m.Type != "match" {
			continue
		}
		rel, _ := filepath.Rel(workdir, m.Data.Path.Text)
		line := strings.TrimRight(m.Data.Lines.Text, "\n")
		out = append(out, fmt.Sprintf("%s:%d:%s", rel, m.Data.LineNumber, line))
		if len(out) >= maxMatches {
			break
		}
	}
	return out, scanner.Err()
}
