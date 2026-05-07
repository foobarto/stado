package runtime

// EP-no-internal-tools — installNativeToolImports is now a no-op.
//
// This file used to register the stado_fs_tool_* / stado_exec_bash /
// stado_http_get / stado_search_* / stado_lsp_* host imports as
// delegates to native tool.Tool structs (fs.ReadTool, bash.BashTool,
// etc.). Steps 1–7 of EP-no-internal-tools migrated those to true
// primitives or wasm-side rewrites. The native packages went away
// with them.
//
// The function is kept for now as a no-op so host_imports.go's
// InstallHostImports keeps a stable shape; callers don't need to
// know whether the runtime ships any native delegates.

import (
	"github.com/tetratelabs/wazero"
)

func installNativeToolImports(_ wazero.HostModuleBuilder, _ *Host) {
	// no delegates remain; intentional no-op
}
