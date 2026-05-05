package runtime

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerCompressImports(builder wazero.HostModuleBuilder, host *Host) {
	registerCompressImport(builder, host)
	registerDecompressImport(builder, host)
}

// stado_compress(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_cap) → int32
// Algos: gzip, zlib, zstd (zstd deferred — needs dependency).
func registerCompressImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			dataPtr, dataLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			outPtr, outCap := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])

			if !host.Compress {
				host.Logger.Warn("stado_compress denied: no compress cap")
				stack[0] = api.EncodeI32(-1)
				return
			}
			algo, _ := readStringLimited(mod, algoPtr, algoLen, 32)
			data, err := readBytesLimited(mod, dataPtr, dataLen, uint32(maxPluginRuntimeFSFileBytes))
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			compressed, err := compress(algo, data)
			if err != nil {
				host.Logger.Warn("stado_compress failed",
					slog.String("algo", algo), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, compressed))
		}),
		[]api.ValueType{
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
		},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_compress")
}

// stado_decompress — same signature as stado_compress, inverse operation.
func registerDecompressImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			dataPtr, dataLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			outPtr, outCap := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])

			if !host.Compress {
				stack[0] = api.EncodeI32(-1)
				return
			}
			algo, _ := readStringLimited(mod, algoPtr, algoLen, 32)
			data, err := readBytesLimited(mod, dataPtr, dataLen, uint32(maxPluginRuntimeFSFileBytes))
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			decompressed, err := decompress(algo, data)
			if err != nil {
				host.Logger.Warn("stado_decompress failed",
					slog.String("algo", algo), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, decompressed))
		}),
		[]api.ValueType{
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
		},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_decompress")
}

func compress(algo string, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	switch algo {
	case "gzip":
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	case "zlib":
		w := zlib.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("compress: unknown algo %q", algo)
	}
	return buf.Bytes(), nil
}

func decompress(algo string, data []byte) ([]byte, error) {
	switch algo {
	case "gzip":
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, maxPluginRuntimeFSFileBytes))
	case "zlib":
		r, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, maxPluginRuntimeFSFileBytes))
	default:
		return nil, fmt.Errorf("decompress: unknown algo %q", algo)
	}
}
