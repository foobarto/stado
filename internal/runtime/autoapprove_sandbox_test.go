package runtime

import (
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// autoApproveHost must implement tool.SandboxPolicyProvider so the caller's
// AgentLoopOptions.DefaultSandboxPolicy actually reaches the wasm
// confinement path (pluginrun asserts h.(tool.SandboxPolicyProvider)).
var _ tool.SandboxPolicyProvider = autoApproveHost{}

// TestAutoApproveHost_SandboxPolicyOptIn: the auto-created host confines bash
// only when its caller supplies a default policy.
// Model A (decision 2026-06-13).
func TestAutoApproveHost_SandboxPolicyOptIn(t *testing.T) {
	// An omitted policy remains omitted.
	var operator autoApproveHost
	if operator.DefaultSandboxPolicy() != nil {
		t.Error("default autoApproveHost must not invent a sandbox policy")
	}

	// A supplied policy is returned verbatim so pluginrun applies it.
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
