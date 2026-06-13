package acp

import (
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// *acpHost must implement tool.SandboxPolicyProvider — that interface is the
// ONLY channel that confines bash (shell.exec → stado_exec) for wasm tools
// (pluginrun asserts h.(tool.SandboxPolicyProvider)). Before Model A
// (decision 2026-06-13) acpHost lacked it, so a Zed/ACP client's bash ran
// unconfined. Compile-time guard: removing the method breaks the build.
var _ tool.SandboxPolicyProvider = (*acpHost)(nil)

// TestACPHost_ConfinesBashByDefault: acp is an autonomous surface, so its host
// returns a non-nil default sandbox policy → bash is confined by default
// (matching mcp-server / daemon).
func TestACPHost_ConfinesBashByDefault(t *testing.T) {
	h := &acpHost{workdir: t.TempDir()}
	if h.DefaultSandboxPolicy() == nil {
		t.Fatal("acpHost.DefaultSandboxPolicy() must return a non-nil policy so bash is confined")
	}
}
