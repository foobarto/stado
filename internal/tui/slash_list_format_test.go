package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// leadingSpaces counts the run of leading spaces on a line — the column
// a hanging continuation line begins at.
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}

// TestToolLs_ShowsDescription: before the shared formatter, `/tool ls`
// printed only "name<pad>state" — the tool description never surfaced.
// It must now render the description (and continue to mark autoloaded
// tools).
func TestToolLs_ShowsDescription(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	// Narrow to a single tool so we can assert on its description without
	// scanning the whole registry; ad_acl_abuse has a long body that the
	// old "%-32s %s" form never showed.
	m.handleToolSlash([]string{"/tool", "ls", "ad_acl_abuse"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "ad_acl_abuse") {
		t.Fatalf("/tool ls missing the tool name: %q", out)
	}
	// A representative word from the description must appear — it was
	// entirely absent before this change.
	if !strings.Contains(out, "principals") {
		t.Errorf("/tool ls should now render the tool DESCRIPTION (word 'principals' missing): %q", out)
	}
	// No ellipsis: the formatter wraps rather than truncates.
	if strings.Contains(out, "…") {
		t.Errorf("/tool ls must not truncate the description with an ellipsis: %q", out)
	}
}

// TestToolLs_AutoloadedMarked: an autoloaded tool carries the
// "(autoloaded)" marker in its rendered row.
func TestToolLs_AutoloadedMarked(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"ad_acl_abuse"}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "ls", "ad_acl_abuse"})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "(autoloaded)") {
		t.Errorf("/tool ls should mark autoloaded tools: %q", out)
	}
}

// TestSkillList_HangIndentsDescription: a /skill list with a long
// description must hang-indent the wrapped continuation lines at the
// gutter, not flow them back to column 0.
func TestSkillList_HangIndentsDescription(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	longDesc := "reproduce the failing behaviour as a red test before touching any " +
		"production code then make the smallest change that turns it green and sweep " +
		"for sibling bugs that share the same root cause"
	body := "---\nname: bugfix\ndescription: " + longDesc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "bugfix.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newSkillModel(t, root)
	// Force a realistic-but-narrow panel so the long description wraps.
	m.vp.SetWidth(60)
	m.handleSkillSlash([]string{"/skill"})
	out := m.lastSystemBlockBody()

	if !strings.Contains(out, "/skill:bugfix") {
		t.Fatalf("/skill list missing the command: %q", out)
	}
	// The description must wrap (multi-line) AND every continuation line of
	// the skill row must be indented past column 0 (hanging indent).
	lines := strings.Split(out, "\n")
	var sawHang bool
	for _, ln := range lines {
		// A continuation line carries description words but not the
		// "/skill:" name and is indented.
		if strings.Contains(ln, "root cause") || strings.Contains(ln, "sibling") {
			if !strings.HasPrefix(ln, "/skill:") {
				if leadingSpaces(ln) == 0 {
					t.Errorf("wrapped /skill description flowed to column 0: %q", ln)
				} else {
					sawHang = true
				}
			}
		}
	}
	if !sawHang {
		t.Errorf("expected a hang-indented continuation line in /skill output:\n%s", out)
	}
	if strings.Contains(out, "…") {
		t.Errorf("/skill must not truncate the description: %q", out)
	}
}
