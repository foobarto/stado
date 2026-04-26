// Package lspfind wraps the LSP client as agent-callable tools.
//
// v1: find_definition for Go files via gopls. Hover + references + document
// symbols + workspace symbols follow once gopls startup cost is amortised
// across a session-long client pool.
package lspfind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

// FindDefinition exposes textDocument/definition as a stado tool.
//
// Language support is picked by file extension → language-server name. The
// underlying LSP client is cached per-workdir so repeated calls in the same
// session don't pay the gopls startup penalty.
type FindDefinition struct {
	mu      sync.Mutex
	clients map[string]*lsp.Client // workdir → live client
}

func (f *FindDefinition) Name() string { return "find_definition" }
func (f *FindDefinition) Description() string {
	return "LSP textDocument/definition — jump to the declaration of a symbol at path:line:column."
}

func (f *FindDefinition) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Relative to workdir"},
			"line":   map[string]any{"type": "integer", "description": "1-indexed line"},
			"column": map[string]any{"type": "integer", "description": "1-indexed column"},
		},
		"required": []string{"path", "line", "column"},
	}
}

func (f *FindDefinition) Class() tool.Class { return tool.ClassNonMutating }

type findArgs struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func (f *FindDefinition) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var a findArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if a.Path == "" || a.Line <= 0 || a.Column <= 0 {
		return tool.Result{Error: "path, line (>=1) and column (>=1) are required"},
			errors.New("lspfind: bad args")
	}

	full, err := workdirpath.Resolve(h.Workdir(), a.Path, false)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	server := serverFor(filepath.Ext(a.Path))
	if server == "" {
		return tool.Result{Error: fmt.Sprintf("no LSP server configured for %q", filepath.Ext(a.Path))},
			fmt.Errorf("lspfind: no server for %s", a.Path)
	}

	cli, err := f.clientFor(ctx, h.Workdir(), server)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	text, err := readLSPDocumentText(h.Workdir(), a.Path)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if err := cli.DidOpen(full, languageIDFor(filepath.Ext(a.Path)), text); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	locs, err := cli.Definition(ctx, full, lsp.Position{
		Line: a.Line - 1, Character: a.Column - 1,
	})
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(locs) == 0 {
		return tool.Result{Content: "No definition found"}, nil
	}
	var b strings.Builder
	for _, l := range locs {
		path := lsp.URIToPath(l.URI)
		rel, _ := filepath.Rel(h.Workdir(), path)
		fmt.Fprintf(&b, "%s:%d:%d\n", rel, l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	return tool.Result{Content: truncateLSPOutput(b.String())}, nil
}

// Close shuts down every cached LSP client. Call on session teardown.
func (f *FindDefinition) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.clients {
		_ = c.Close()
	}
	f.clients = nil
}

func (f *FindDefinition) clientFor(ctx context.Context, workdir, server string) (*lsp.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clients == nil {
		f.clients = map[string]*lsp.Client{}
	}
	key := workdir + "|" + server
	if c, ok := f.clients[key]; ok {
		return c, nil
	}
	c, err := lsp.Launch(ctx, server, workdir)
	if err != nil {
		return nil, err
	}
	f.clients[key] = c
	return c, nil
}

// serverFor maps a file extension to the language-server binary name.
func serverFor(ext string) string {
	switch ext {
	case ".go":
		return "gopls"
	case ".rs":
		return "rust-analyzer"
	case ".py":
		return "pyright"
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript-language-server"
	}
	return ""
}

func languageIDFor(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	}
	return "plaintext"
}
