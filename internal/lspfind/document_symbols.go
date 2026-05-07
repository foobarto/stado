package lspfind

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
)

// DocumentSymbols runs textDocument/documentSymbol — file outline.
func DocumentSymbols(ctx context.Context, args SymbolsArgs, workdir string) (string, error) {
	if args.Path == "" {
		return "", errors.New("lspfind: path required")
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

	syms, err := cli.DocumentSymbols(ctx, full)
	if err != nil {
		return "", err
	}
	if len(syms) == 0 {
		return "", nil
	}
	var b strings.Builder
	renderSymbols(&b, syms, 0)
	return truncateLSPOutput(b.String()), nil
}

// renderSymbols prints a hierarchical outline: function/class lines with
// indented children underneath. Kind numbers come from the LSP SymbolKind
// enum (File=1, Module=2, Namespace=3, Package=4, Class=5, Method=6,
// Property=7, Field=8, Constructor=9, Enum=10, Interface=11, Function=12,
// Variable=13, Constant=14, …); we show the common-case labels and fall
// back to numeric for the rest.
func renderSymbols(b *strings.Builder, syms []lsp.DocumentSymbol, depth int) {
	for _, s := range syms {
		indent := strings.Repeat("  ", depth)
		kind := symbolKindLabel(s.Kind)
		fmt.Fprintf(b, "%s[%s] %s (L%d-L%d)\n", indent, kind, s.Name, s.Range.Start.Line+1, s.Range.End.Line+1)
		if len(s.Children) > 0 {
			renderSymbols(b, s.Children, depth+1)
		}
	}
}

func symbolKindLabel(k int) string {
	switch k {
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "prop"
	case 8:
		return "field"
	case 9:
		return "ctor"
	case 10:
		return "enum"
	case 11:
		return "iface"
	case 12:
		return "func"
	case 13:
		return "var"
	case 14:
		return "const"
	case 22:
		return "struct"
	case 23:
		return "event"
	}
	return fmt.Sprintf("kind%d", k)
}
