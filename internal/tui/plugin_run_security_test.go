package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/toolinput"
)

func TestRunPluginToolAsyncRejectsOversizedArgsBeforeRuntime(t *testing.T) {
	cmd := runPluginToolAsync(nil, "", nil, plugins.ToolDef{Name: "compact"},
		strings.Repeat("x", toolinput.MaxBytes+1), "test-plugin", nil, nil, nil)

	msg, ok := cmd().(pluginRunResultMsg)
	if !ok {
		t.Fatalf("message type = %T", msg)
	}
	if !strings.Contains(msg.errMsg, "tool input exceeds") {
		t.Fatalf("errMsg = %q", msg.errMsg)
	}
}
