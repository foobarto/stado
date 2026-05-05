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
	if !strings.Contains(out, "usage") {
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

func TestToolDisable_NoSave(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"shell.exec"} // pre-existing autoload
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "disable", "shell.exec"})

	if !containsString(m.sessionToolOverrides.disableAdd, "shell.exec") {
		t.Errorf("disable should add to disableAdd; got %+v", m.sessionToolOverrides)
	}
	// Disable must also pull from autoload (in-memory only — disk
	// config.Tools.Autoload stays untouched).
	eff := m.effectiveConfig()
	if containsString(eff.Tools.Autoload, "shell.exec") {
		t.Errorf("disable should mask autoload; got effective autoload %v", eff.Tools.Autoload)
	}
}

func TestToolDisable_NoArgs(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	m.handleToolSlash([]string{"/tool", "disable"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "usage") {
		t.Errorf("missing-args should print usage; got: %q", out)
	}
}

func TestToolDisable_PullsFromEnableRemove(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "disable", "shell.exec"})
	if !containsString(m.sessionToolOverrides.enableRemove, "shell.exec") {
		t.Errorf("disable should populate enableRemove for the same arg; got %+v", m.sessionToolOverrides)
	}
}

func TestToolAutoload_NoSave(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "autoload", "fs.read"})

	if !containsString(m.sessionToolOverrides.autoloadAdd, "fs.read") {
		t.Errorf("autoload should add; got %+v", m.sessionToolOverrides)
	}
}

func TestToolAutoload_NoArgs(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	m.handleToolSlash([]string{"/tool", "autoload"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "usage") {
		t.Errorf("missing-args should print usage; got: %q", out)
	}
}

func TestToolAutoload_ClearsPendingRemove(t *testing.T) {
	// If a previous /tool unautoload queued a removal, /tool autoload
	// for the same name should cancel that removal.
	m := &Model{cfg: &config.Config{}}
	m.sessionToolOverrides.autoloadRemove = []string{"fs.read"}
	m.handleToolSlash([]string{"/tool", "autoload", "fs.read"})

	if containsString(m.sessionToolOverrides.autoloadRemove, "fs.read") {
		t.Errorf("autoload should clear pending autoloadRemove; got %v", m.sessionToolOverrides.autoloadRemove)
	}
	if !containsString(m.sessionToolOverrides.autoloadAdd, "fs.read") {
		t.Errorf("autoload should populate autoloadAdd; got %v", m.sessionToolOverrides.autoloadAdd)
	}
}

func TestToolUnautoload_NoSave(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs.read"}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "unautoload", "fs.read"})

	eff := m.effectiveConfig()
	if containsString(eff.Tools.Autoload, "fs.read") {
		t.Errorf("unautoload should remove from effective autoload; got %v", eff.Tools.Autoload)
	}
	// Disk config untouched.
	if !containsString(cfg.Tools.Autoload, "fs.read") {
		t.Errorf("disk config should still have fs.read; got %v", cfg.Tools.Autoload)
	}
}

func TestToolUnautoload_NoArgs(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	m.handleToolSlash([]string{"/tool", "unautoload"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "usage") {
		t.Errorf("missing-args should print usage; got: %q", out)
	}
}

func TestToolUnautoload_ClearsPendingAdd(t *testing.T) {
	// If /tool autoload queued an add, /tool unautoload for the same
	// name should cancel that add.
	m := &Model{cfg: &config.Config{}}
	m.sessionToolOverrides.autoloadAdd = []string{"fs.read"}
	m.handleToolSlash([]string{"/tool", "unautoload", "fs.read"})

	if containsString(m.sessionToolOverrides.autoloadAdd, "fs.read") {
		t.Errorf("unautoload should clear pending autoloadAdd; got %v", m.sessionToolOverrides.autoloadAdd)
	}
	if !containsString(m.sessionToolOverrides.autoloadRemove, "fs.read") {
		t.Errorf("unautoload should populate autoloadRemove; got %v", m.sessionToolOverrides.autoloadRemove)
	}
}
