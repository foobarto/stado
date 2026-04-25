package runtime

import (
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestBuildExecutorRegistersTasksToolBeforeFiltering(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"tasks"}

	exec, err := BuildExecutor(nil, cfg, "test")
	if err != nil {
		t.Fatalf("BuildExecutor: %v", err)
	}
	all := exec.Registry.All()
	if len(all) != 1 {
		t.Fatalf("tools = %d, want 1: %v", len(all), all)
	}
	if all[0].Name() != "tasks" {
		t.Fatalf("tool = %q, want tasks", all[0].Name())
	}
}
