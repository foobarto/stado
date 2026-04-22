package lspfind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

// DocumentSymbols implements textDocument/documentSymbol.
type DocumentSymbols struct {
	Definition *FindDefinition
}

func (d *DocumentSymbols) Name() string { return "document_symbols" }
func (d *DocumentSymbols) Description() string {
	return "File outline: functions, types, methods with their line ranges."
}
func (d *DocumentSymbols) Class() tool.Class { return tool.ClassNonMutating }

func (d *DocumentSymbols) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
}

type symArgs struct {
	Path string `json:"path"`
}

func (d *DocumentSymbols) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var a symArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if a.Path == "" {
		return tool.Result{Error: "path required"}, errors.New("lspfind: path required")
	}
	full, err := workdirpath.Resolve(h.Workdir(), a.Path, false)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	server := serverFor(filepath.Ext(a.Path))
	if server == "" {
		return tool.Result{Error: "no LSP server for this extension"}, fmt.Errorf("no server")
	}
	cli, err := d.Definition.clientFor(ctx, h.Workdir(), server)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	_ = cli.DidOpen(full, languageIDFor(filepath.Ext(a.Path)), string(data))

	syms, err := cli.DocumentSymbols(ctx, full)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(syms) == 0 {
		return tool.Result{Content: "No symbols"}, nil
	}
	var b strings.Builder
	renderSymbols(&b, syms, 0)
	return tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
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
