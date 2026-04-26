package runtime

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	maxPluginRuntimeFSPathBytes uint32 = 4 << 10
	maxPluginRuntimeFSFileBytes int64  = 16 << 20
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
			if pathLen > maxPluginRuntimeFSPathBytes {
				host.Logger.Warn("stado_fs_read denied — path too large", slog.Uint64("path_len", uint64(pathLen)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			path, err := readStringLimited(mod, pathPtr, pathLen, maxPluginRuntimeFSPathBytes)
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
			data, err := readAllowedFile(abs, host.FSRead, pluginFSReadLimit(bufCap))
			if err != nil {
				host.Logger.Warn("stado_fs_read failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if uint64(len(data)) > uint64(bufCap) {
				host.Logger.Warn("stado_fs_read truncation",
					slog.String("path", abs),
					slog.Int("data_bytes", len(data)),
					slog.Uint64("buf_cap", uint64(bufCap)))
				stack[0] = api.EncodeI32(-1)
				return
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
			if pathLen > maxPluginRuntimeFSPathBytes {
				host.Logger.Warn("stado_fs_write denied — path too large", slog.Uint64("path_len", uint64(pathLen)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			if int64(bufLen) > maxPluginRuntimeFSFileBytes {
				host.Logger.Warn("stado_fs_write denied — payload too large", slog.Uint64("buf_len", uint64(bufLen)))
				stack[0] = api.EncodeI32(-1)
				return
			}
			path, err := readStringLimited(mod, pathPtr, pathLen, maxPluginRuntimeFSPathBytes)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			abs, err := realPathForWrite(host.Workdir, path)
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
			data, err := readBytesLimited(mod, bufPtr, bufLen, uint32(maxPluginRuntimeFSFileBytes))
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if err := writeAllowedFile(abs, host.FSWrite, data, 0o644); err != nil {
				host.Logger.Warn("stado_fs_write failed", slog.String("path", abs), slog.String("err", err.Error()))
				stack[0] = api.EncodeI32(-1)
				return
			}
			encoded, ok := encodeI32Length(len(data))
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}
			stack[0] = encoded
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

func realPathForWrite(workdir, path string) (string, error) {
	abs := resolveAbs(workdir, path)
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)
	resolvedParent, err := realPath(workdir, parent)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(resolvedParent, base)), nil
}

func readAllowedFile(abs string, allow []string, maxBytes int64) ([]byte, error) {
	root, rel, err := openAllowedRoot(abs, allow, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if maxBytes > maxPluginRuntimeFSFileBytes {
		maxBytes = maxPluginRuntimeFSFileBytes
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin fs read target is not a regular file: %s", abs)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("plugin fs read exceeds %d bytes: %s", maxBytes, abs)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("plugin fs read exceeds %d bytes: %s", maxBytes, abs)
	}
	return data, nil
}

func writeAllowedFile(abs string, allow []string, data []byte, perm os.FileMode) error {
	if int64(len(data)) > maxPluginRuntimeFSFileBytes {
		return fmt.Errorf("plugin fs write exceeds %d bytes: %s", maxPluginRuntimeFSFileBytes, abs)
	}
	root, rel, err := openAllowedRoot(abs, allow, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if dir := filepath.Dir(rel); dir != "." {
		if err := workdirpath.MkdirAllRootNoSymlink(root, dir, 0o755); err != nil {
			return err
		}
	}
	return workdirpath.WriteRootFileAtomic(root, rel, data, perm)
}

func pluginFSReadLimit(bufCap uint32) int64 {
	limit := int64(bufCap)
	if limit > maxPluginRuntimeFSFileBytes {
		return maxPluginRuntimeFSFileBytes
	}
	return limit
}

func openAllowedRoot(abs string, allow []string, allowMissing bool) (*os.Root, string, error) {
	abs = filepath.Clean(abs)
	for _, allowed := range allow {
		allowed = filepath.Clean(allowed)
		if !pathAllowed(abs, []string{allowed}) {
			continue
		}
		rootPath, err := rootedAllowedPath(allowed, allowMissing)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootPath, abs)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		root, err := workdirpath.OpenRootNoSymlink(rootPath)
		if err != nil {
			return nil, "", err
		}
		return root, rel, nil
	}
	return nil, "", os.ErrPermission
}

func rootedAllowedPath(allowed string, allowMissing bool) (string, error) {
	resolved, err := filepath.EvalSymlinks(allowed)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !allowMissing || !os.IsNotExist(err) {
		return "", err
	}
	dir := filepath.Clean(allowed)
	for dir != string(filepath.Separator) && dir != "." {
		if _, statErr := os.Stat(dir); statErr == nil {
			return filepath.EvalSymlinks(dir)
		}
		dir = filepath.Dir(dir)
	}
	return filepath.EvalSymlinks(dir)
}

func encodeI32Length(n int) (uint64, bool) {
	if n > maxInt32ResultInt {
		return 0, false
	}
	return api.EncodeI32(int32(n)), true // #nosec G115 -- checked against maxInt32Result.
}
