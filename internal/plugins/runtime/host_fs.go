package runtime

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func registerFSImports(builder wazero.HostModuleBuilder, host *Host) {
	registerFSReadImport(builder, host)
	registerFSWriteImport(builder, host)
}

func registerFSReadImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_fs_read(path_ptr, path_len, buf_ptr, buf_cap) → int32
	//
	// Returns bytes written to buf, -1 on capability deny / read error
	// / truncation past buf_cap. Path is resolved relative to
	// host.Workdir when not absolute, then capability-checked against
	// the manifest's fs:read entries.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			pathPtr := api.DecodeU32(stack[0])
			pathLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufCap := api.DecodeU32(stack[3])
			path, err := readString(mod, pathPtr, pathLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			abs, err := realPath(host.Workdir, path)
			if err != nil {
				host.Logger.Warn("stado_fs_read denied — symlink resolution failed", slog.String("path", path), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.allowRead(abs) {
				host.Logger.Warn("stado_fs_read denied", slog.String("path", abs))
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				host.Logger.Warn("stado_fs_read failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if uint32(len(data)) > bufCap {
				data = data[:bufCap]
			}
			stack[0] = api.EncodeI32(writeBytes(mod, bufPtr, bufCap, data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_fs_read")
}

func registerFSWriteImport(builder wazero.HostModuleBuilder, host *Host) {
	// stado_fs_write(path_ptr, path_len, buf_ptr, buf_len) → int32
	//
	// Returns bytes written, -1 on deny / error. Always creates or
	// truncates the target; append mode isn't exposed.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			pathPtr := api.DecodeU32(stack[0])
			pathLen := api.DecodeU32(stack[1])
			bufPtr := api.DecodeU32(stack[2])
			bufLen := api.DecodeU32(stack[3])
			path, err := readString(mod, pathPtr, pathLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			abs, err := realPath(host.Workdir, path)
			if err != nil {
				host.Logger.Warn("stado_fs_write denied — symlink resolution failed", slog.String("path", path), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if !host.allowWrite(abs) {
				host.Logger.Warn("stado_fs_write denied", slog.String("path", abs))
				stack[0] = api.EncodeI32(-1)
				return
			}
			data, err := readBytes(mod, bufPtr, bufLen)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				host.Logger.Warn("stado_fs_write mkdir", slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := os.WriteFile(abs, data, 0o644); err != nil {
				host.Logger.Warn("stado_fs_write failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = api.EncodeI32(int32(len(data)))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_fs_write")
}

// resolveAbs makes a plugin-supplied path absolute. Plugins that pass
// relative paths see them rooted at workdir; absolute paths pass
// through. Cleaned via filepath.Clean so `..` traversal is normalised
// — the capability check still rejects paths that escape the allowed
// roots, but cleaning first avoids prefix-bypass with "/allowed/../etc".
//
// SECURITY: returns the cleaned absolute path *before* following symlinks.
// The caller must use realPath() if symlink resolution is needed; for
// fs_read/fs_write the capability check runs on the resolved target so
// symlinks cannot escape the allowed prefix.
func resolveAbs(workdir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	return filepath.Clean(path)
}

// realPath returns the canonical filesystem path: absolute, cleaned,
// and with symlink components evaluated. If the path (or any parent
// component) resolves outside the allowed prefix, the returned string
// still indicates that escape so the caller can reject it.
//
// SECURITY: used by fs_read/fs_write so that a symlink inside an allowed
// directory pointing outside is caught.  The capability check runs
// against the resolved target, not the symlink path.
func realPath(workdir, path string) (string, error) {
	abs := resolveAbs(workdir, path)

	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}

	// If the final component doesn't exist yet (legitimate for writes),
	// resolve as far as possible by walking up to the deepest existing
	// ancestor, resolving that, and appending the remaining suffix.
	if !os.IsNotExist(err) {
		return "", err
	}

	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	// Walk up until we find an existing directory.
	for dir != "/" && dir != "." {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		dir, base = filepath.Dir(dir), filepath.Join(filepath.Base(dir), base)
	}

	resolvedDir, derr := filepath.EvalSymlinks(dir)
	if derr != nil {
		return "", derr
	}
	result := filepath.Join(resolvedDir, base)
	return filepath.Clean(result), nil
}
