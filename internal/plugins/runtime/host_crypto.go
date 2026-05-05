package runtime

import (
	"context"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"log/slog"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerCryptoImports(builder wazero.HostModuleBuilder, host *Host) {
	registerHashImport(builder, host)
	registerHMACImport(builder, host)
}

// stado_hash(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_cap) → int32
// Returns hex-encoded digest. Algos: md5, sha1, sha256, sha512.
// blake3 deferred until a pure-Go dependency is chosen.
func registerHashImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			dataPtr, dataLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			outPtr, outCap := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])

			if !host.CryptoHash {
				host.Logger.Warn("stado_hash denied: no crypto:hash cap")
				stack[0] = api.EncodeI32(-1)
				return
			}
			algo, err := readStringLimited(mod, algoPtr, algoLen, 32)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := readBytesLimited(mod, dataPtr, dataLen, uint32(maxPluginRuntimeFSFileBytes))
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			newFn, err := hashNewFn(algo)
			if err != nil {
				host.Logger.Warn("stado_hash unknown algo", slog.String("algo", algo))
				stack[0] = api.EncodeI32(-1)
				return
			}
			h := newFn()
			h.Write(data)
			digest := hex.EncodeToString(h.Sum(nil))
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, []byte(digest)))
		}),
		[]api.ValueType{
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
		},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_hash")
}

// stado_hmac(algo_ptr, algo_len, key_ptr, key_len, data_ptr, data_len, out_ptr, out_cap) → int32
func registerHMACImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			algoPtr, algoLen := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
			keyPtr, keyLen := api.DecodeU32(stack[2]), api.DecodeU32(stack[3])
			dataPtr, dataLen := api.DecodeU32(stack[4]), api.DecodeU32(stack[5])
			outPtr, outCap := api.DecodeU32(stack[6]), api.DecodeU32(stack[7])

			if !host.CryptoHash {
				stack[0] = api.EncodeI32(-1)
				return
			}
			algo, err := readStringLimited(mod, algoPtr, algoLen, 32)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			key, err := readBytesLimited(mod, keyPtr, keyLen, 4<<10)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := readBytesLimited(mod, dataPtr, dataLen, uint32(maxPluginRuntimeFSFileBytes))
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			newFn, err := hashNewFn(algo)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			mac := hmac.New(newFn, key) //nolint:gosec
			mac.Write(data)
			digest := hex.EncodeToString(mac.Sum(nil))
			stack[0] = api.EncodeI32(writeBytes(mod, outPtr, outCap, []byte(digest)))
		}),
		[]api.ValueType{
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
		},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_hmac")
}

// hashNewFn returns the hash.New function for the named algorithm.
func hashNewFn(algo string) (func() hash.Hash, error) {
	switch algo {
	case "md5":
		return md5.New, nil //nolint:gosec
	case "sha1":
		return sha1.New, nil //nolint:gosec
	case "sha256":
		return sha256.New, nil
	case "sha512":
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("stado_hash: unknown algo %q", algo)
	}
}
