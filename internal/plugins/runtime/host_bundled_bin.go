package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/tools/astgrep"
	"github.com/foobarto/stado/internal/tools/rg"
)

// bundledBinPath returns the filesystem path to a named bundled binary.
// Supported names: "ripgrep", "ast-grep". Falls back to PATH lookup.
// The extraction + caching logic lives in the per-tool packages; this
// function is the single dispatch point for stado_bundled_bin. EP-0038 §B.
func bundledBinPath(host *Host, name string) (string, error) {
	if !host.BundledBin {
		return "", fmt.Errorf("bundled-bin:%s capability required", name)
	}
	switch name {
	case "ripgrep", "rg":
		return rg.BundledPath()
	case "ast-grep", "astgrep", "sg":
		return astgrep.BundledPath()
	default:
		return "", fmt.Errorf("stado_bundled_bin: unknown binary %q", name)
	}
}

func registerBundledBinImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_bundled_bin(name_ptr, name_len, buf_ptr, buf_cap) → int32
	// Returns the path to the extracted binary as UTF-8 bytes.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			namePtr := api.DecodeU32(stack[0])
			nameLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufCap := api.DecodeU32(stack[3])

			name, err := readStringLimited(mod, namePtr, nameLen, 256)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			path, err := bundledBinPath(host, name)
			if err != nil {
				host.Logger.Warn("stado_bundled_bin failed",
					slog.String("name", name), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, []byte(path)))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_bundled_bin")
}
