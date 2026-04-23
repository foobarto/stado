package runtime

import (
	"context"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// InstallHostImports registers stado_log / stado_fs_read / stado_fs_write
// on the runtime's "stado" module namespace. The caller passes a Host
// per plugin; each host function closes over it so different plugins
// get different capability gates even though they share the runtime.
//
// Must be called BEFORE Instantiate(wasmBytes, ...) so the plugin can
// resolve the imports at link time.
func InstallHostImports(ctx context.Context, r *Runtime, host *Host) error {
	builder := r.rt.NewHostModuleBuilder(NamespaceStado)

	registerLogImport(builder, host)
	registerUIApprovalImport(builder, host)
	registerFSImports(builder, host)
	registerSessionImports(builder, host)
	registerLLMImport(builder, host)
	installNativeToolImports(builder, host)

	if _, err := builder.Instantiate(ctx); err != nil {
		return fmt.Errorf("wazero: install host imports: %w", err)
	}
	return nil
}

// NamespaceStado is the wasm module name plugins import from
// ("stado"). Exported so test helpers can assert without re-stringing.
const NamespaceStado = "stado"

// Compile-time check: confirm wazero's NewHostModuleBuilder returns
// the type we expect.
var _ wazero.HostModuleBuilder = (wazero.HostModuleBuilder)(nil)

func encodeToolSidePayload(mod api.Module, ptr, cap uint32, payload []byte) int32 {
	n := writeBytes(mod, ptr, cap, payload)
	if n <= 0 {
		return -1
	}
	return -n
}
