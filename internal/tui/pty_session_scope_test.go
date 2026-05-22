package tui

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// TestResetForSession_TearsDownPTYs: switching sessions must destroy the prior
// session's PTYs so the incoming session can't access them (#015).
func TestResetForSession_TearsDownPTYs(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	if m.ptyManager == nil {
		t.Skip("model has no pty manager")
	}
	if _, err := m.ptyManager.Spawn(pty.SpawnOpts{Cmd: "sleep 30"}); err != nil {
		t.Skipf("Spawn needs a runnable shell: %v", err)
	}
	if len(m.ptyManager.List()) == 0 {
		t.Fatal("precondition: a PTY should be live before the switch")
	}
	m.resetForSession(m.session)
	if n := len(m.ptyManager.List()); n != 0 {
		t.Errorf("session switch must tear down PTYs (#015); %d still live", n)
	}
}
