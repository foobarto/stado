package headless

import (
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
)

func TestServerBuildExecutorAppliesSurfaceSandbox(t *testing.T) {
	cfg := &config.Config{}

	t.Run("broker ceiling", func(t *testing.T) {
		s := NewServer(cfg, nil)
		s.ExecutorSandbox = runtime.ExecutorSandbox{
			Ceiling:        sandbox.Policy{FSWrite: []string{"/work"}},
			EnforceCeiling: true,
		}
		exec, err := s.buildExecutor(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := exec.Runner.(*sandbox.CeilingRunner); !ok {
			t.Fatalf("runner = %T, want *sandbox.CeilingRunner", exec.Runner)
		}
	})

	t.Run("explicit opt out", func(t *testing.T) {
		s := NewServer(cfg, nil)
		s.ExecutorSandbox = runtime.ExecutorSandbox{Disabled: true}
		exec, err := s.buildExecutor(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := exec.Runner.(sandbox.NoneRunner); !ok {
			t.Fatalf("runner = %T, want sandbox.NoneRunner", exec.Runner)
		}
		if got := s.defaultSandboxPolicy(t.TempDir()); got != nil {
			t.Fatalf("disabled default policy = %T, want nil", got)
		}
	})
}
