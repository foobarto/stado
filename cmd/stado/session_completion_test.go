package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// completionEnv seeds a clean-room cfg with N sessions, optionally
// attaching descriptions. Returns the config + cleanup.
func completionEnv(t *testing.T, ids []string, descs map[string]string) (*config.Config, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)
	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, _ := openSidecar(cfg)
	for _, id := range ids {
		if _, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	for id, d := range descs {
		_ = runtime.WriteDescription(filepath.Join(cfg.WorktreeDir(), id), d)
	}
	return cfg, restore
}

// TestCompleteSessionIDs_ListsAllWhenEmptyPrefix: an empty
// toComplete returns every session id.
func TestCompleteSessionIDs_ListsAllWhenEmptyPrefix(t *testing.T) {
	_, restore := completionEnv(t,
		[]string{"aaa-1", "bbb-2", "ccc-3"}, nil)
	defer restore()

	comps, dir := completeSessionIDs(nil, nil, "")
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", dir)
	}
	if len(comps) != 3 {
		t.Fatalf("expected 3 completions, got %d: %v", len(comps), comps)
	}
}

// TestCompleteSessionIDs_PrefixFilter: toComplete narrows results to
// ids starting with the prefix.
func TestCompleteSessionIDs_PrefixFilter(t *testing.T) {
	_, restore := completionEnv(t,
		[]string{"aaa-1", "aaa-2", "bbb-3"}, nil)
	defer restore()

	comps, _ := completeSessionIDs(nil, nil, "aaa")
	if len(comps) != 2 {
		t.Errorf("prefix 'aaa' should yield 2; got %d: %v", len(comps), comps)
	}
	for _, c := range comps {
		// "id\tdesc" form possible; check just the id part.
		id := strings.SplitN(c, "\t", 2)[0]
		if !strings.HasPrefix(id, "aaa") {
			t.Errorf("completion %q doesn't match prefix 'aaa'", c)
		}
	}
}

// TestCompleteSessionIDs_IncludesDescriptionHint: sessions with
// descriptions return "id\tdescription" so the shell shows the
// label as a completion hint.
func TestCompleteSessionIDs_IncludesDescriptionHint(t *testing.T) {
	_, restore := completionEnv(t,
		[]string{"labelled", "unlabelled"},
		map[string]string{"labelled": "react refactor"})
	defer restore()

	comps, _ := completeSessionIDs(nil, nil, "")
	var found bool
	for _, c := range comps {
		if strings.HasPrefix(c, "labelled\t") && strings.Contains(c, "react refactor") {
			found = true
		}
	}
	if !found {
		t.Errorf("labelled session should include description hint: %v", comps)
	}
}

// TestCompleteSessionIDs_NoMoreAfterFirstArg: once the user has
// already typed a session id, we return no further completions (the
// subcommand doesn't take another id).
func TestCompleteSessionIDs_NoMoreAfterFirstArg(t *testing.T) {
	_, restore := completionEnv(t, []string{"a", "b"}, nil)
	defer restore()

	comps, dir := completeSessionIDs(nil, []string{"a"}, "")
	if len(comps) != 0 {
		t.Errorf("expected no completions for 2nd positional: %v", comps)
	}
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", dir)
	}
}
