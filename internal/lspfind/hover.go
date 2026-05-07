package lspfind

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
)

// Hover runs textDocument/hover — docs/type for the symbol at
// args.Path:args.Line:args.Column (1-indexed).
func Hover(ctx context.Context, args Args, workdir string) (string, error) {
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
	docText, err := readLSPDocumentText(workdir, args.Path)
	if err != nil {
		return "", err
	}
	_ = cli.DidOpen(full, languageIDFor(filepath.Ext(args.Path)), docText)

	text, err := cli.Hover(ctx, full, lsp.Position{
		Line: args.Line - 1, Character: args.Column - 1,
	})
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", nil
	}
	return truncateLSPOutput(text), nil
}
