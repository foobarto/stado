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
// LSP server lifetime is owned by an LSPClientManager (see manager.go):
// one live client per (workdir, language-server) tuple, amortised across
// calls, reaped on session teardown via CloseAll. The four package-level
// functions below route through a process-default manager so the wasm
// host imports (internal/plugins/runtime/host_lsp.go) need no change; a
// session that wants its own scoped manager constructs one with
// NewLSPClientManager and calls the *ViaManager helpers.
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

// defaultMgr backs the package-level FindDefinition / FindReferences /
// DocumentSymbols / Hover functions — the path the wasm host imports
// take. It's a process-lifetime manager (session ctx = Background); a
// session that wants servers reaped on its own teardown constructs an
// LSPClientManager with NewLSPClientManager and calls the *ViaManager
// variants, then CloseAll on teardown. The runtime wires the latter.
var (
	defaultMgrOnce sync.Once
	defaultMgr     *LSPClientManager
)

func mgr() *LSPClientManager {
	defaultMgrOnce.Do(func() {
		defaultMgr = NewLSPClientManager(context.Background())
	})
	return defaultMgr
}

// CloseAll shuts down every LSP client cached by the process-default
// manager. Kept as a package-level entry point for callers that don't
// hold a manager handle (e.g. a global shutdown). Session-scoped callers
// should hold their own *LSPClientManager and call its CloseAll instead.
func CloseAll() { mgr().CloseAll() }

// FindDefinition runs textDocument/definition for the symbol at
// `args.Path:args.Line:args.Column` (1-indexed). Returns formatted
// `<rel>:<line>:<col>` matches, or an empty string with nil error
// when no definition was found. Routes through the process-default
// manager; see FindDefinitionViaManager for the session-scoped variant.
func FindDefinition(ctx context.Context, args Args, workdir string) (string, error) {
	return FindDefinitionViaManager(ctx, mgr(), args, workdir)
}

// FindDefinitionViaManager is FindDefinition against an explicit,
// session-scoped manager so the launched servers die with the session.
func FindDefinitionViaManager(ctx context.Context, m *LSPClientManager, args Args, workdir string) (string, error) {
	if args.Path == "" || args.Line <= 0 || args.Column <= 0 {
		return "", errors.New("lspfind: path, line (>=1) and column (>=1) are required")
	}
	r, err := workdirpath.New(workdir)
	if err != nil {
		return "", err
	}
	full, err := r.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	server := serverFor(filepath.Ext(args.Path))
	if server == "" {
		return "", fmt.Errorf("lspfind: no LSP server configured for %q", filepath.Ext(args.Path))
	}
	cli, err := m.ClientFor(ctx, workdir, server)
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

// FindReferences runs textDocument/references via the process-default
// manager; see FindReferencesViaManager for the session-scoped variant.
func FindReferences(ctx context.Context, args RefArgs, workdir string) (string, error) {
	return FindReferencesViaManager(ctx, mgr(), args, workdir)
}

// FindReferencesViaManager is FindReferences against an explicit,
// session-scoped manager.
func FindReferencesViaManager(ctx context.Context, m *LSPClientManager, args RefArgs, workdir string) (string, error) {
	if args.Path == "" || args.Line <= 0 || args.Column <= 0 {
		return "", errors.New("lspfind: path, line (>=1) and column (>=1) are required")
	}
	r, err := workdirpath.New(workdir)
	if err != nil {
		return "", err
	}
	full, err := r.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	server := serverFor(filepath.Ext(args.Path))
	if server == "" {
		return "", fmt.Errorf("lspfind: no LSP server configured for %q", filepath.Ext(args.Path))
	}
	cli, err := m.ClientFor(ctx, workdir, server)
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

func formatWorkdirLocations(workdir string, locs []lsp.Location) string {
	r, err := workdirpath.New(workdir)
	if err != nil {
		return ""
	}
	var b []byte
	for _, l := range locs {
		_, rel, err := r.RootRel(lsp.URIToPath(l.URI))
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
