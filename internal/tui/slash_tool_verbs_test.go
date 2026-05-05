package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestToolEnable_NoSave: /tool enable shell.exec → in-memory override.
func TestToolEnable_NoSave(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "enable", "shell.exec"})

	if !containsString(m.sessionToolOverrides.enableAdd, "shell.exec") {
		t.Errorf("enable should add to session overrides; got %+v", m.sessionToolOverrides)
	}
	// Disk config must NOT be mutated.
	if len(cfg.Tools.Enabled) != 0 {
		t.Errorf("/tool enable without --save shouldn't mutate cfg.Tools.Enabled; got %v", cfg.Tools.Enabled)
	}
}

// TestToolEnable_NoArgs prints usage.
func TestToolEnable_NoArgs(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	m.handleToolSlash([]string{"/tool", "enable"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "usage") && !strings.Contains(out, "/tool enable") {
		t.Errorf("missing-args should print usage; got: %q", out)
	}
}

// TestToolEnable_OutputMentionsSession: feedback message is clear
// about the change being session-only.
func TestToolEnable_OutputMentionsSession(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "enable", "shell.exec"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "session") {
		t.Errorf("message should mention session-scope; got: %q", out)
	}
}

// TestToolEnable_PullsFromDisableRemove: enabling a tool that was
// previously session-disabled should mark it for removal from disabled.
func TestToolEnable_PullsFromDisableRemove(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	// Enable should also queue a removal from disabled (in case the
	// override layer or disk config had it disabled).
	m.handleToolSlash([]string{"/tool", "enable", "shell.exec"})
	if !containsString(m.sessionToolOverrides.disableRemove, "shell.exec") {
		t.Errorf("enable should populate disableRemove for the same arg; got %+v", m.sessionToolOverrides)
	}
}
