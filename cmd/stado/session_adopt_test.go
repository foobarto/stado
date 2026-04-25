package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestSessionAdoptDryRunThenApply(t *testing.T) {
	cfg, parent, child, forkTree, restore := adoptCommandEnv(t)
	defer restore()

	writeWorkFile(t, child.WorktreePath, "child.txt", "child\n")
	commitAndTag(t, child, 1)
	writeWorkFile(t, parent.WorktreePath, "parent.txt", "parent\n")
	commitAndTag(t, parent, 2)

	sessionAdoptApply = false
	sessionAdoptForkTree = forkTree.String()
	sessionAdoptJSON = false
	t.Cleanup(func() {
		sessionAdoptApply = false
		sessionAdoptForkTree = ""
		sessionAdoptJSON = false
		sessionAdoptCmd.SetOut(nil)
	})
	var out bytes.Buffer
	sessionAdoptCmd.SetOut(&out)
	if err := sessionAdoptCmd.RunE(sessionAdoptCmd, []string{parent.ID, child.ID}); err != nil {
		t.Fatalf("dry-run adopt: %v", err)
	}
	if !strings.Contains(out.String(), "status: ready") || !strings.Contains(out.String(), "dry_run: true") {
		t.Fatalf("dry-run output = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(parent.WorktreePath, "child.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created child.txt, stat err = %v", err)
	}

	out.Reset()
	sessionAdoptApply = true
	if err := sessionAdoptCmd.RunE(sessionAdoptCmd, []string{parent.ID, child.ID}); err != nil {
		t.Fatalf("apply adopt: %v", err)
	}
	if !strings.Contains(out.String(), "status: applied") {
		t.Fatalf("apply output = %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(parent.WorktreePath, "child.txt"))
	if err != nil {
		t.Fatalf("read adopted child.txt: %v", err)
	}
	if string(data) != "child\n" {
		t.Fatalf("adopted child.txt = %q", data)
	}
	if head, err := parent.TraceHead(); err != nil || head.IsZero() {
		t.Fatalf("trace head missing after apply in %s: %s %v", cfg.WorktreeDir(), head, err)
	}
}

func TestSessionAdoptApplyReportsConflicts(t *testing.T) {
	_, parent, child, forkTree, restore := adoptCommandEnv(t)
	defer restore()

	writeWorkFile(t, child.WorktreePath, "same.txt", "child\n")
	commitAndTag(t, child, 1)
	writeWorkFile(t, parent.WorktreePath, "same.txt", "parent\n")
	commitAndTag(t, parent, 2)

	sessionAdoptApply = true
	sessionAdoptForkTree = forkTree.String()
	sessionAdoptJSON = false
	t.Cleanup(func() {
		sessionAdoptApply = false
		sessionAdoptForkTree = ""
		sessionAdoptJSON = false
		sessionAdoptCmd.SetOut(nil)
	})
	var out bytes.Buffer
	sessionAdoptCmd.SetOut(&out)
	err := sessionAdoptCmd.RunE(sessionAdoptCmd, []string{parent.ID, child.ID})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v, want conflict", err)
	}
	if !strings.Contains(out.String(), "status: blocked") || !strings.Contains(out.String(), "same.txt") {
		t.Fatalf("conflict output = %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(parent.WorktreePath, "same.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "parent\n" {
		t.Fatalf("parent same.txt = %q, want parent", data)
	}
}

func adoptCommandEnv(t *testing.T) (*config.Config, *stadogit.Session, *stadogit.Session, plumbing.Hash, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	restore := chdir(t, cwd)
	cfg, err := config.Load()
	if err != nil {
		restore()
		t.Fatalf("config.Load: %v", err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		restore()
		t.Fatalf("worktree dir: %v", err)
	}
	sc, err := openSidecar(cfg)
	if err != nil {
		restore()
		t.Fatalf("openSidecar: %v", err)
	}
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "adopt-parent", plumbing.ZeroHash)
	if err != nil {
		restore()
		t.Fatalf("CreateSession parent: %v", err)
	}
	writeWorkFile(t, parent.WorktreePath, "base.txt", "base\n")
	commitAndTag(t, parent, 1)
	child, err := runtime.ForkSession(cfg, parent)
	if err != nil {
		restore()
		t.Fatalf("ForkSession: %v", err)
	}
	forkTree, err := child.CurrentTree()
	if err != nil {
		restore()
		t.Fatalf("child.CurrentTree: %v", err)
	}
	return cfg, parent, child, forkTree, restore
}
