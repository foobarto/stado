package runtime

import (
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// autoApproveHost must implement tool.SandboxPolicyProvider so the headless
// opt-in (AgentLoopOptions.DefaultSandboxPolicy) actually reaches the wasm
// confinement path (pluginrun asserts h.(tool.SandboxPolicyProvider)).
var _ tool.SandboxPolicyProvider = autoApproveHost{}

// TestAutoApproveHost_SandboxPolicyOptIn: the auto-created host confines bash
// ONLY when a default policy is supplied (autonomous surfaces like headless).
// run / tui leave it nil → bash keeps operator's-FS semantics (unconfined).
// Model A (decision 2026-06-13).
func TestAutoApproveHost_SandboxPolicyOptIn(t *testing.T) {
	// Operator surfaces (run / tui): no opt-in → no confinement.
	var operator autoApproveHost
	if operator.DefaultSandboxPolicy() != nil {
		t.Error("default autoApproveHost (run/tui) must NOT confine bash — operator's-FS semantics")
	}

	// Autonomous surface (headless): opt-in → the supplied policy is returned
	// verbatim so pluginrun applies it.
	sentinel := struct{ tag string }{"policy"}
	confined := autoApproveHost{defaultSandboxPolicy: sentinel}
	got := confined.DefaultSandboxPolicy()
	if got == nil {
		t.Fatal("autoApproveHost with a default policy set must confine bash")
	}
	if got != any(sentinel) {
		t.Errorf("DefaultSandboxPolicy() must return the supplied policy verbatim; got %v", got)
	}
}
