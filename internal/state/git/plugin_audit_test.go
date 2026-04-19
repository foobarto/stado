package git

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestCommitMeta_PluginTrailer asserts the Plugin field surfaces as a
// `Plugin: <name>` trailer at the end of the commit message. DESIGN
// §"Plugin extension points for context management" invariant 3
// requires every plugin-triggered action to be auditable — the
// trailer is what makes `git log --grep=Plugin:` work.
func TestCommitMeta_PluginTrailer(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-plugin", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	h, err := sess.CommitToTrace(CommitMeta{
		Tool:     "llm_invoke",
		ShortArg: "summarise prior turns",
		Summary:  "plugin LLM call",
		Plugin:   "auto-compactor",
		Agent:    "plugin:auto-compactor",
		Turn:     3,
	})
	if err != nil {
		t.Fatalf("CommitToTrace: %v", err)
	}
	c, err := object.GetCommit(sc.repo.Storer, h)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Message, "\nPlugin: auto-compactor\n") {
		t.Errorf("message missing Plugin trailer: %q", c.Message)
	}
	if !strings.Contains(c.Message, "Agent: plugin:auto-compactor") {
		t.Errorf("message missing agent attribution: %q", c.Message)
	}
}

// TestCommitMeta_NoPluginTrailer_WhenEmpty — core agent-loop commits
// don't set Plugin, so the trailer must stay off (would pollute
// `git log --grep=Plugin:` with false positives otherwise).
func TestCommitMeta_NoPluginTrailer_WhenEmpty(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-noplugin", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := sess.CommitToTrace(CommitMeta{Tool: "grep", Summary: "no plugin"})
	c, _ := object.GetCommit(sc.repo.Storer, h)
	if strings.Contains(c.Message, "Plugin:") {
		t.Errorf("expected no Plugin trailer when field is empty, got: %q", c.Message)
	}
}
