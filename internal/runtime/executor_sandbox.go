package runtime

import (
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
)

// ExecutorSandbox is the surface-independent sandbox decision applied to an
// executor. The CLI derives it once from the broker session and explicit
// --no-sandbox flag, then passes it into every executor factory owned by a
// long-lived surface.
type ExecutorSandbox struct {
	Ceiling        sandbox.Policy
	EnforceCeiling bool
	Disabled       bool
}

// Apply updates exec's runner to match the configured sandbox decision.
func (p ExecutorSandbox) Apply(exec *tools.Executor) {
	if exec == nil {
		return
	}
	exec.Runner = p.Runner(exec.Runner)
}

// Runner applies the explicit opt-out or broker ceiling to inner.
func (p ExecutorSandbox) Runner(inner sandbox.Runner) sandbox.Runner {
	if p.Disabled {
		return sandbox.NoneRunner{}
	}
	if p.EnforceCeiling {
		return sandbox.NewCeilingRunner(inner, p.Ceiling)
	}
	return inner
}

// DefaultSandboxPolicy returns the host policy used for wasm process imports.
// A non-nil policy is necessary for stado_exec/stado_proc_spawn to route
// through the host's runner; explicit opt-out keeps their legacy direct-exec
// path.
func (p ExecutorSandbox) DefaultSandboxPolicy(workdir string) any {
	if p.Disabled {
		return nil
	}
	return pluginRuntime.NewDefaultSandboxPolicy(workdir)
}
