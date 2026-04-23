package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestSessionDescribe_RejectsTraversalID(t *testing.T) {
	_, _, restore := describeEnv(t)
	defer restore()

	describeClear = false
	err := sessionDescribeCmd.RunE(sessionDescribeCmd, []string{"../../escape", "x"})
	if err == nil {
		t.Fatal("expected traversal id to fail")
	}
	if !strings.Contains(err.Error(), "invalid session id") {
		t.Fatalf("expected invalid session id error, got %v", err)
	}
}

func TestAgentsKill_RejectsTraversalID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(cfg.WorktreeDir(), "..", "..", "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = agentsKillCmd.RunE(agentsKillCmd, []string{"../../victim"})
	if err == nil {
		t.Fatal("expected traversal id to fail")
	}
	if !strings.Contains(err.Error(), "invalid session id") {
		t.Fatalf("expected invalid session id error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Fatalf("victim path was modified: %v", err)
	}
}

func TestPluginRun_RejectsTraversalID(t *testing.T) {
	_ = isolatedHome(t)
	err := pluginRunCmd.RunE(pluginRunCmd, []string{"../escape", "compact"})
	if err == nil {
		t.Fatal("expected traversal plugin id to fail")
	}
	if !strings.Contains(err.Error(), "invalid plugin id") {
		t.Fatalf("expected invalid plugin id error, got %v", err)
	}
}
