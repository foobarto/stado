// Package lspfind is the engine behind the four stado_lsp_* host imports
// (find_definition, find_references, document_symbols, hover).
//
// EP-no-internal-tools Step 6: this used to live under
// `internal/tools/lspfind` and implement `tool.Tool` so the four host
// imports could delegate. After Step 6 it's a primitive subsystem
// package — no `tool.Tool` interface, no model surface. Plain
// `FindDefinition(ctx, args, workdir) (string, error)` etc. The host
// wrapper at `internal/plugins/runtime/host_lsp.go` reads args, calls
// the corresponding lspfind function, encodes the response.
//
// Per-workdir LSP client cache lives at package level (was a struct
// field with a mutex on each *FindDefinition). One client per
// (workdir, language-server) pair, amortised across calls. Call
// CloseAll on session teardown.
package lspfind

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
)

// Args is the JSON-decoded shape for the position-aware tools
// (find_definition, find_references, hover). DocumentSymbols uses
// SymbolsArgs.
type Args struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// RefArgs is FindReferences' args — Args + an optional include-decl
// flag.
type RefArgs struct {
	Args
	IncludeDeclaration *bool `json:"include_declaration"`
}

// SymbolsArgs is DocumentSymbols' args — just a path.
type SymbolsArgs struct {
	Path string `json:"path"`
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*lsp.Client{} // "<workdir>|<server>" → live client
)

// FindDefinition runs textDocument/definition for the symbol at
// `args.Path:args.Line:args.Column` (1-indexed). Returns formatted
// `<rel>:<line>:<col>` matches, or an empty string with nil error
// when no definition was found.
func FindDefinition(ctx context.Context, args Args, workdir string) (string, error) {
	if args.Path == "" || args.Line <= 0 || args.Column <= 0 {
		return "", errors.New("lspfind: path, line (>=1) and column (>=1) are required")
	}
	full, err := workdirpath.Resolve(workdir, args.Path, false)
	if err != nil {
		return "", err
	}
	server := serverFor(filepath.Ext(args.Path))
	if server == "" {
		return "", fmt.Errorf("lspfind: no LSP server configured for %q", filepath.Ext(args.Path))
	}
	cli, err := clientFor(ctx, workdir, server)
	if err != nil {
		return "", err
	}
	text, err := readLSPDocumentText(workdir, args.Path)
	if err != nil {
		return "", err
	}
	if err := cli.DidOpen(full, languageIDFor(filepath.Ext(args.Path)), text); err != nil {
		return "", err
	}
	locs, err := cli.Definition(ctx, full, lsp.Position{
		Line: args.Line - 1, Character: args.Column - 1,
	})
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "", nil
	}
	out := formatWorkdirLocations(workdir, locs)
	return out, nil
}

// FindReferences runs textDocument/references.
func FindReferences(ctx context.Context, args RefArgs, workdir string) (string, error) {
	if args.Path == "" || args.Line <= 0 || args.Column <= 0 {
		return "", errors.New("lspfind: path, line (>=1) and column (>=1) are required")
	}
	full, err := workdirpath.Resolve(workdir, args.Path, false)
	if err != nil {
		return "", err
	}
	server := serverFor(filepath.Ext(args.Path))
	if server == "" {
		return "", fmt.Errorf("lspfind: no LSP server configured for %q", filepath.Ext(args.Path))
	}
	cli, err := clientFor(ctx, workdir, server)
	if err != nil {
		return "", err
	}
	text, err := readLSPDocumentText(workdir, args.Path)
	if err != nil {
		return "", err
	}
	_ = cli.DidOpen(full, languageIDFor(filepath.Ext(args.Path)), text)

	include := true
	if args.IncludeDeclaration != nil {
		include = *args.IncludeDeclaration
	}
	locs, err := cli.References(ctx, full, lsp.Position{
		Line: args.Line - 1, Character: args.Column - 1,
	}, include)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "", nil
	}
	return formatWorkdirLocations(workdir, locs), nil
}

// CloseAll shuts down every cached LSP client. Call on session
// teardown to avoid leaking gopls/rust-analyzer processes.
func CloseAll() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
	clients = map[string]*lsp.Client{}
}

func clientFor(ctx context.Context, workdir, server string) (*lsp.Client, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	key := workdir + "|" + server
	if c, ok := clients[key]; ok {
		return c, nil
	}
	c, err := lsp.Launch(ctx, server, workdir)
	if err != nil {
		return nil, err
	}
	clients[key] = c
	return c, nil
}

func formatWorkdirLocations(workdir string, locs []lsp.Location) string {
	var b []byte
	for _, l := range locs {
		_, rel, err := workdirpath.RootRel(workdir, lsp.URIToPath(l.URI), false)
		if err != nil {
			continue
		}
		b = append(b, []byte(fmt.Sprintf("%s:%d:%d\n",
			filepath.ToSlash(rel), l.Range.Start.Line+1, l.Range.Start.Character+1))...)
	}
	if len(b) == 0 {
		return ""
	}
	return truncateLSPOutput(string(b))
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
