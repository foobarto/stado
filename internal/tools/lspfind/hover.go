package lspfind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

// Hover implements textDocument/hover.
type Hover struct {
	Definition *FindDefinition
}

func (h *Hover) Name() string { return "hover" }
func (h *Hover) Description() string {
	return "LSP textDocument/hover — docs/type for a symbol at path:line:column."
}
func (h *Hover) Class() tool.Class { return tool.ClassNonMutating }

func (h *Hover) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"line":   map[string]any{"type": "integer"},
			"column": map[string]any{"type": "integer"},
		},
		"required": []string{"path", "line", "column"},
	}
}

type hoverArgs struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func (h *Hover) Run(ctx context.Context, raw json.RawMessage, host tool.Host) (tool.Result, error) {
	var a hoverArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if a.Path == "" || a.Line <= 0 || a.Column <= 0 {
		return tool.Result{Error: "path, line, column required"}, errors.New("lspfind: bad args")
	}
	full, err := workdirpath.Resolve(host.Workdir(), a.Path, false)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	server := serverFor(filepath.Ext(a.Path))
	if server == "" {
		return tool.Result{Error: "no LSP server for this extension"}, fmt.Errorf("no server")
	}
	cli, err := h.Definition.clientFor(ctx, host.Workdir(), server)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	docText, err := readLSPDocumentText(host.Workdir(), a.Path)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	_ = cli.DidOpen(full, languageIDFor(filepath.Ext(a.Path)), docText)

	text, err := cli.Hover(ctx, full, lsp.Position{Line: a.Line - 1, Character: a.Column - 1})
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if text == "" {
		return tool.Result{Content: "No hover information"}, nil
	}
	return tool.Result{Content: truncateLSPOutput(text)}, nil
}
