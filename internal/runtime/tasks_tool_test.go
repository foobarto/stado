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
	// The meta-tool kernel always survives filtering (EP-0037); assert on the
	// non-meta surface — only the tasks tool should remain.
	var nonMeta []string
	for _, tl := range exec.Registry.All() {
		if IsMetaTool(tl.Name()) {
			continue
		}
		nonMeta = append(nonMeta, tl.Name())
	}
	if len(nonMeta) != 1 || nonMeta[0] != "tasks" {
		t.Fatalf("non-meta tools = %v, want [tasks]", nonMeta)
	}
}
