// stado_lsp_* — true primitives (no tool.Tool delegation).
//
// EP-no-internal-tools Step 6: this used to live under tool_imports.go
// as four delegates to lspfind tool structs. The structs are gone; the
// engine moved to internal/lspfind/ with package-level client cache.
// These four host imports are wasm-facing wrappers: read args, gate by
// the lsp:query capability + fs:read scope, call the corresponding
// lspfind function, encode the response.
//
// Capability gates:
//   - lsp:query (h.LSPQuery == true)
//   - fs:read on the resolved path (each call's args.Path goes through
//     the host's allowRead check via lspfind's workdirpath.Resolve)

package runtime

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/lspfind"
)

func registerLSPImports(builder wazero.HostModuleBuilder, host *Host) {
	registerLSPFindDefinitionImport(builder, host)
	registerLSPFindReferencesImport(builder, host)
	registerLSPDocumentSymbolsImport(builder, host)
	registerLSPHoverImport(builder, host)
}

func registerLSPFindDefinitionImport(builder wazero.HostModuleBuilder, host *Host) {
	registerLSPPositionalImport(builder, host, "stado_lsp_find_definition",
		func(ctx context.Context, args lspfind.Args, workdir string) (string, error) {
			return lspfind.FindDefinition(ctx, args, workdir)
		})
}

func registerLSPHoverImport(builder wazero.HostModuleBuilder, host *Host) {
	registerLSPPositionalImport(builder, host, "stado_lsp_hover",
		func(ctx context.Context, args lspfind.Args, workdir string) (string, error) {
			return lspfind.Hover(ctx, args, workdir)
		})
}

// registerLSPPositionalImport wires find_definition + hover — the two
// imports that take {path, line, column}. References has an extra
// include_declaration flag and is registered separately.
func registerLSPPositionalImport(builder wazero.HostModuleBuilder, host *Host, exportName string,
	fn func(context.Context, lspfind.Args, string) (string, error)) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])

			if !host.LSPQuery || len(host.FSRead) == 0 {
				msg := []byte(exportName + " denied: needs lsp:query + fs:read caps")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, maxPluginRuntimeToolArgsBytes)
			if err != nil {
				host.Logger.Warn(exportName+" args read failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			var args lspfind.Args
			if err := json.Unmarshal(argsBytes, &args); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			out, err := fn(ctx, args, host.Workdir)
			if err != nil {
				host.Logger.Warn(exportName+" failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if byteLenExceedsCap([]byte(out), resCap) {
				msg := []byte(exportName + ": result exceeds buffer cap")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, []byte(out)))
		}),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export(exportName)
}

func registerLSPFindReferencesImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])

			if !host.LSPQuery || len(host.FSRead) == 0 {
				msg := []byte("stado_lsp_find_references denied: needs lsp:query + fs:read caps")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, maxPluginRuntimeToolArgsBytes)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			var args lspfind.RefArgs
			if err := json.Unmarshal(argsBytes, &args); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			out, err := lspfind.FindReferences(ctx, args, host.Workdir)
			if err != nil {
				host.Logger.Warn("stado_lsp_find_references failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if byteLenExceedsCap([]byte(out), resCap) {
				msg := []byte("stado_lsp_find_references: result exceeds buffer cap")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, []byte(out)))
		}),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_lsp_find_references")
}

func registerLSPDocumentSymbolsImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			argsPtr := api.DecodeU32(stack[0])
			argsLen := api.DecodeU32(stack[1])
			resPtr := api.DecodeU32(stack[2])
			resCap := api.DecodeU32(stack[3])

			if !host.LSPQuery || len(host.FSRead) == 0 {
				msg := []byte("stado_lsp_document_symbols denied: needs lsp:query + fs:read caps")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			argsBytes, err := readBytesLimited(mod, argsPtr, argsLen, maxPluginRuntimeToolArgsBytes)
			if err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			var args lspfind.SymbolsArgs
			if err := json.Unmarshal(argsBytes, &args); err != nil {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			out, err := lspfind.DocumentSymbols(ctx, args, host.Workdir)
			if err != nil {
				host.Logger.Warn("stado_lsp_document_symbols failed", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, []byte(err.Error())))
				return
			}
			if byteLenExceedsCap([]byte(out), resCap) {
				msg := []byte("stado_lsp_document_symbols: result exceeds buffer cap")
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, resPtr, resCap, msg))
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, resPtr, resCap, []byte(out)))
		}),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_lsp_document_symbols")
}
