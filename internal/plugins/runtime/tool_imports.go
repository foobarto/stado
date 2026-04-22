package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/tools/astgrep"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/lspfind"
	"github.com/foobarto/stado/internal/tools/readctx"
	"github.com/foobarto/stado/internal/tools/rg"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/pkg/tool"
)

type toolImportSpec struct {
	exportName string
	tool       tool.Tool
	allowed    func(*Host) bool
	preflight  func(*Host, json.RawMessage) error
}

func installNativeToolImports(builder wazero.HostModuleBuilder, host *Host) {
	def := &lspfind.FindDefinition{}
	specs := []toolImportSpec{
		{exportName: "stado_fs_tool_read", tool: fs.ReadTool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 }, preflight: preflightReadPath},
		{exportName: "stado_fs_tool_write", tool: fs.WriteTool{}, allowed: func(h *Host) bool { return len(h.FSWrite) > 0 }, preflight: preflightWritePath},
		{exportName: "stado_fs_tool_edit", tool: fs.EditTool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && len(h.FSWrite) > 0 }, preflight: preflightEditPath},
		{exportName: "stado_fs_tool_glob", tool: fs.GlobTool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 }, preflight: requireFullReadScope},
		{exportName: "stado_fs_tool_grep", tool: fs.GrepTool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 }, preflight: requireFullReadScope},
		{exportName: "stado_fs_tool_read_context", tool: readctx.Tool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 }, preflight: requireFullReadScope},
		{exportName: "stado_exec_bash", tool: bash.BashTool{}, allowed: func(h *Host) bool { return h.ExecBash }},
		{exportName: "stado_http_get", tool: webfetch.WebFetchTool{}, allowed: func(h *Host) bool { return h.NetHTTPGet || len(h.NetHost) > 0 }, preflight: preflightHTTPGet},
		{exportName: "stado_search_ripgrep", tool: rg.Tool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && h.ExecSearch }, preflight: requireFullReadScope},
		{exportName: "stado_search_ast_grep", tool: astgrep.Tool{}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && len(h.FSWrite) > 0 && h.ExecASTGrep }, preflight: requireFullReadWriteScope},
		{exportName: "stado_lsp_find_definition", tool: def, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && h.LSPQuery }, preflight: requireFullReadScope},
		{exportName: "stado_lsp_find_references", tool: &lspfind.FindReferences{Definition: def}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && h.LSPQuery }, preflight: requireFullReadScope},
		{exportName: "stado_lsp_document_symbols", tool: &lspfind.DocumentSymbols{Definition: def}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && h.LSPQuery }, preflight: requireFullReadScope},
		{exportName: "stado_lsp_hover", tool: &lspfind.Hover{Definition: def}, allowed: func(h *Host) bool { return len(h.FSRead) > 0 && h.LSPQuery }, preflight: requireFullReadScope},
	}
	for _, spec := range specs {
		spec := spec
		if !spec.allowed(host) {
			continue
		}
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
				argsPtr := api.DecodeU32(stack[0])
				argsLen := api.DecodeU32(stack[1])
				resultPtr := api.DecodeU32(stack[2])
				resultCap := api.DecodeU32(stack[3])
				if host.ToolHost == nil {
					msg := []byte("plugin host has no tool runtime context")
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resultPtr, resultCap, msg))
					return
				}
				argsBytes, err := readBytes(mod, argsPtr, argsLen)
				if err != nil {
					host.Logger.Warn(spec.exportName+" args read failed", slog.String("err", err.Error()))
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resultPtr, resultCap, []byte(err.Error())))
					return
				}
				if spec.preflight != nil {
					if err := spec.preflight(host, json.RawMessage(argsBytes)); err != nil {
						host.Logger.Warn(spec.exportName+" preflight denied", slog.String("err", err.Error()))
						stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resultPtr, resultCap, []byte(err.Error())))
						return
					}
				}
				res, err := spec.tool.Run(ctx, json.RawMessage(argsBytes), host.ToolHost)
				if err != nil {
					host.Logger.Warn(spec.exportName+" failed", slog.String("err", err.Error()))
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resultPtr, resultCap, []byte(err.Error())))
					return
				}
				if res.Error != "" {
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resultPtr, resultCap, []byte(res.Error)))
					return
				}
				content := []byte(res.Content)
				if uint32(len(content)) > resultCap {
					msg := []byte(fmt.Sprintf("%s: result %d exceeds %d-byte cap", spec.exportName, len(content), resultCap))
					stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resultPtr, resultCap, msg))
					return
				}
				stack[0] = api.EncodeI32(writeBytes(mod, resultPtr, resultCap, content))
			}), []api.ValueType{
				api.ValueTypeI32, api.ValueTypeI32,
				api.ValueTypeI32, api.ValueTypeI32,
			}, []api.ValueType{api.ValueTypeI32}).
			Export(spec.exportName)
	}

}

func preflightReadPath(h *Host, raw json.RawMessage) error {
	var args fs.ReadArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
	}
	full, err := realPath(h.Workdir, args.Path)
	if err != nil {
		return err
	}
	if !h.allowRead(full) {
		return fmt.Errorf("read path %q denied by manifest", args.Path)
	}
	return nil
}

func preflightEditPath(h *Host, raw json.RawMessage) error {
	var args fs.EditArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
	}
	full, err := realPath(h.Workdir, args.Path)
	if err != nil {
		return err
	}
	if !h.allowRead(full) || !h.allowWrite(full) {
		return fmt.Errorf("edit path %q denied by manifest", args.Path)
	}
	return nil
}

func preflightWritePath(h *Host, raw json.RawMessage) error {
	var args fs.WriteArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
	}
	full, err := realPath(h.Workdir, args.Path)
	if err != nil {
		return err
	}
	if !h.allowWrite(full) {
		return fmt.Errorf("write path %q denied by manifest", args.Path)
	}
	return nil
}

func requireFullReadScope(h *Host, _ json.RawMessage) error {
	root, err := realPath(h.Workdir, ".")
	if err != nil {
		return err
	}
	if !h.allowRead(root) {
		return fmt.Errorf("manifest must grant fs:read to the full workdir for this wrapper")
	}
	return nil
}

func requireFullReadWriteScope(h *Host, _ json.RawMessage) error {
	root, err := realPath(h.Workdir, ".")
	if err != nil {
		return err
	}
	if !h.allowRead(root) || !h.allowWrite(root) {
		return fmt.Errorf("manifest must grant fs:read and fs:write to the full workdir for this wrapper")
	}
	return nil
}

func preflightHTTPGet(h *Host, raw json.RawMessage) error {
	var args webfetch.Args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
	}
	u, err := url.Parse(args.URL)
	if err != nil {
		return err
	}
	if len(h.NetHost) == 0 {
		return nil
	}
	hostName := strings.ToLower(u.Hostname())
	for _, allowed := range h.NetHost {
		if strings.EqualFold(strings.TrimSpace(allowed), hostName) {
			return nil
		}
	}
	return fmt.Errorf("url host %q denied by manifest", hostName)
}
