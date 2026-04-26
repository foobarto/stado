package lspfind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

// FindReferences implements textDocument/references. Shares its LSP client
// cache with FindDefinition via the exported SharedClients map so gopls /
// rust-analyzer startup cost is amortised across tools.
type FindReferences struct {
	Definition *FindDefinition // reuse the client cache
}

func (f *FindReferences) Name() string { return "find_references" }
func (f *FindReferences) Description() string {
	return "LSP textDocument/references — every usage of a symbol."
}
func (f *FindReferences) Class() tool.Class { return tool.ClassNonMutating }

func (f *FindReferences) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":                map[string]any{"type": "string"},
			"line":                map[string]any{"type": "integer"},
			"column":              map[string]any{"type": "integer"},
			"include_declaration": map[string]any{"type": "boolean", "description": "default true"},
		},
		"required": []string{"path", "line", "column"},
	}
}

type refArgs struct {
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration *bool  `json:"include_declaration"`
}

func (f *FindReferences) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var a refArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if a.Path == "" || a.Line <= 0 || a.Column <= 0 {
		return tool.Result{Error: "path, line (>=1), column (>=1) required"}, errors.New("lspfind: bad args")
	}

	full, err := workdirpath.Resolve(h.Workdir(), a.Path, false)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	server := serverFor(filepath.Ext(a.Path))
	if server == "" {
		return tool.Result{Error: fmt.Sprintf("no LSP server for %q", filepath.Ext(a.Path))},
			fmt.Errorf("lspfind: no server for %s", a.Path)
	}
	cli, err := f.Definition.clientFor(ctx, h.Workdir(), server)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	text, err := readLSPDocumentText(h.Workdir(), a.Path)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	_ = cli.DidOpen(full, languageIDFor(filepath.Ext(a.Path)), text)

	include := true
	if a.IncludeDeclaration != nil {
		include = *a.IncludeDeclaration
	}
	locs, err := cli.References(ctx, full, lsp.Position{Line: a.Line - 1, Character: a.Column - 1}, include)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(locs) == 0 {
		return tool.Result{Content: "No references found"}, nil
	}
	var b strings.Builder
	for _, l := range locs {
		path := lsp.URIToPath(l.URI)
		rel, _ := filepath.Rel(h.Workdir(), path)
		fmt.Fprintf(&b, "%s:%d:%d\n", rel, l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	return tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}
