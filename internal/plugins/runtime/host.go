package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/plugins"
)

// Host is the capability-gated bridge exposed to a plugin's wasm
// module. It owns the sandbox policy (derived from the manifest) and
// the slog.Logger used by stado_log.
//
// One Host per plugin instantiation — the capability lists in it are
// the manifest's, not the process's. Instantiate() builds + registers
// an instance of this type on the runtime before the wasm module runs.
type Host struct {
	Manifest plugins.Manifest
	Logger   *slog.Logger

	// Parsed from Manifest.Capabilities. These are the authoritative
	// allow-lists host-import calls check against. Empty slices mean
	// "deny all" — matches the strict default of plugin execution.
	FSRead  []string
	FSWrite []string
	NetHost []string // allowed hostnames; empty → no net
	Workdir string   // CWD the plugin sees for relative paths
}

// NewHost parses a manifest's capabilities into a Host.
func NewHost(m plugins.Manifest, workdir string, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Host{
		Manifest: m,
		Logger:   logger.With("plugin", m.Name),
		Workdir:  workdir,
	}
	for _, cap := range m.Capabilities {
		parts := strings.SplitN(cap, ":", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "fs":
			if len(parts) != 3 {
				continue
			}
			switch parts[1] {
			case "read":
				h.FSRead = append(h.FSRead, parts[2])
			case "write":
				h.FSWrite = append(h.FSWrite, parts[2])
			}
		case "net":
			// "net:<host>" — the cap string after "net:" is the
			// exact host to allow. Special values "allow" / "deny" are
			// handled at the MCP layer but aren't exposed to plugins
			// here (too much power).
			rest := strings.Join(parts[1:], ":")
			if rest != "" && rest != "allow" && rest != "deny" {
				h.NetHost = append(h.NetHost, rest)
			}
		}
	}
	return h
}

// allowRead / allowWrite perform the capability check. Current
// matching is prefix-based on absolute paths — a manifest entry of
// `/home/user/projects` allows any file under that tree. Glob support
// can be added later if real plugins ask for it.
func (h *Host) allowRead(abs string) bool  { return pathAllowed(abs, h.FSRead) }
func (h *Host) allowWrite(abs string) bool { return pathAllowed(abs, h.FSWrite) }

func pathAllowed(abs string, allow []string) bool {
	for _, a := range allow {
		if a == abs || strings.HasPrefix(abs, strings.TrimRight(a, "/")+"/") {
			return true
		}
	}
	return false
}

// InstallHostImports registers stado_log / stado_fs_read / stado_fs_write
// on the runtime's "stado" module namespace. The caller passes a Host
// per plugin; each host function closes over it so different plugins
// get different capability gates even though they share the runtime.
//
// Must be called BEFORE Instantiate(wasmBytes, ...) so the plugin can
// resolve the imports at link time.
func InstallHostImports(ctx context.Context, r *Runtime, host *Host) error {
	builder := r.rt.NewHostModuleBuilder("stado")

	// stado_log(level_ptr, level_len, msg_ptr, msg_len)
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			levelPtr := api.DecodeU32(stack[0])
			levelLen := api.DecodeU32(stack[1])
			msgPtr := api.DecodeU32(stack[2])
			msgLen := api.DecodeU32(stack[3])
			level, err := readString(mod, levelPtr, levelLen)
			if err != nil {
				return
			}
			msg, err := readString(mod, msgPtr, msgLen)
			if err != nil {
				return
			}
			switch strings.ToLower(level) {
			case "debug":
				host.Logger.Debug(msg)
			case "warn", "warning":
				host.Logger.Warn(msg)
			case "error":
				host.Logger.Error(msg)
			default:
				host.Logger.Info(msg)
			}
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, nil).
		Export("stado_log")

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
			abs := resolveAbs(host.Workdir, path)
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
			abs := resolveAbs(host.Workdir, path)
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

	if _, err := builder.Instantiate(ctx); err != nil {
		return fmt.Errorf("wazero: install host imports: %w", err)
	}
	return nil
}

// resolveAbs makes a plugin-supplied path absolute. Plugins that pass
// relative paths see them rooted at workdir; absolute paths pass
// through. Cleaned via filepath.Clean so `..` traversal is normalised
// — the capability check still rejects paths that escape the allowed
// roots, but cleaning first avoids prefix-bypass with "/allowed/../etc".
func resolveAbs(workdir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	return filepath.Clean(path)
}

// NamespaceStado is the wasm module name plugins import from
// ("stado"). Exported so test helpers can assert without re-stringing.
const NamespaceStado = "stado"

// Compile-time check: confirm wazero's NewHostModuleBuilder returns
// the type we expect.
var _ wazero.HostModuleBuilder = (wazero.HostModuleBuilder)(nil)
