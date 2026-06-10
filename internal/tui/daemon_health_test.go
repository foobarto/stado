package tui

import (
	"strings"
	"testing"
)

func TestDaemonStatusLabel(t *testing.T) {
	m := &Model{}
	if got := m.daemonStatusLabel(); got != "" {
		t.Errorf("unknown should be empty, got %q", got)
	}
	m.daemonHealth = daemonHealthUp
	if got := m.daemonStatusLabel(); got != "up" {
		t.Errorf("up label = %q", got)
	}
	m.daemonHealth = daemonHealthDown
	if got := m.daemonStatusLabel(); got != "down" {
		t.Errorf("down label = %q", got)
	}
}

// TestStatusBarShowsDaemon: the status bar renders the daemon indicator
// once a probe has reported a state, and omits it while unknown.
func TestStatusBarShowsDaemon(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// Unknown: no "daemon" segment.
	if strings.Contains(m.renderStatus(120), "daemon") {
		t.Error("status bar shows daemon segment before any probe")
	}
	m.daemonHealth = daemonHealthDown
	if !strings.Contains(m.renderStatus(120), "daemon") {
		t.Error("status bar missing daemon segment when down")
	}
	m.daemonHealth = daemonHealthUp
	if !strings.Contains(m.renderStatus(120), "daemon") {
		t.Error("status bar missing daemon segment when up")
	}
}
