package runtime

import (
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tools"
)

func TestPinInvokeExecutor_WiresPluginTools(t *testing.T) {
	cfg := &config.Config{}
	reg := BuildDefaultRegistry(cfg)
	exec := &tools.Executor{Registry: reg, Agent: "test"}
	PinInvokeExecutor(reg, exec)

	var bundled, installed, override int
	for _, tool := range reg.All() {
		inner := tool
		if rt, ok := inner.(*renamedTool); ok {
			inner = rt.inner
		}
		switch v := inner.(type) {
		case *bundledPluginTool:
			if v.invokeExec != exec {
				t.Errorf("bundled tool %s missing invokeExec", v.Name())
			}
			bundled++
		case *installedPluginTool:
			if v.invokeExec != exec {
				t.Errorf("installed tool %s missing invokeExec", v.Name())
			}
			installed++
		case *pluginOverrideTool:
			if v.invokeExec != exec {
				t.Errorf("override tool %s missing invokeExec", v.Name())
			}
			override++
		}
	}
	if bundled == 0 {
		t.Fatal("expected bundled plugin tools in default registry")
	}
}
