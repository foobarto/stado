package runtime

import (
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
)

func TestExecutorSandboxApply(t *testing.T) {
	ceiling := sandbox.Policy{FSWrite: []string{"/work"}}

	t.Run("active ceiling wraps runner", func(t *testing.T) {
		exec := &tools.Executor{Runner: sandbox.NoneRunner{}}
		ExecutorSandbox{Ceiling: ceiling, EnforceCeiling: true}.Apply(exec)
		wrapped, ok := exec.Runner.(*sandbox.CeilingRunner)
		if !ok {
			t.Fatalf("runner = %T, want *sandbox.CeilingRunner", exec.Runner)
		}
		if len(wrapped.Ceiling.FSWrite) != 1 || wrapped.Ceiling.FSWrite[0] != "/work" {
			t.Fatalf("ceiling = %#v", wrapped.Ceiling)
		}
	})

	t.Run("disabled selects none runner", func(t *testing.T) {
		exec := &tools.Executor{Runner: sandbox.Detect()}
		ExecutorSandbox{Ceiling: ceiling, EnforceCeiling: true, Disabled: true}.Apply(exec)
		if _, ok := exec.Runner.(sandbox.NoneRunner); !ok {
			t.Fatalf("runner = %T, want sandbox.NoneRunner", exec.Runner)
		}
	})

	t.Run("skipped decision preserves runner", func(t *testing.T) {
		original := sandbox.NoneRunner{}
		exec := &tools.Executor{Runner: original}
		ExecutorSandbox{}.Apply(exec)
		if _, ok := exec.Runner.(sandbox.NoneRunner); !ok {
			t.Fatalf("runner = %T, want sandbox.NoneRunner", exec.Runner)
		}
	})

	t.Run("nil executor is safe", func(t *testing.T) {
		ExecutorSandbox{EnforceCeiling: true}.Apply(nil)
	})

	t.Run("default process policy follows opt out", func(t *testing.T) {
		if got := (ExecutorSandbox{}).DefaultSandboxPolicy("/work"); got == nil {
			t.Fatal("sandboxed executor should provide a default process policy")
		}
		if got := (ExecutorSandbox{Disabled: true}).DefaultSandboxPolicy("/work"); got != nil {
			t.Fatalf("disabled default process policy = %T, want nil", got)
		}
	})
}
