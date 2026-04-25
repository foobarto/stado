package filepicker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestOpenScansCwd: Open collects every regular file under cwd as a
// relative path. Hidden directories and vendor folders are skipped.
func TestOpenScansCwd(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "package x")
	mustWrite(t, filepath.Join(dir, "pkg", "util.go"), "package util")
	mustWrite(t, filepath.Join(dir, "node_modules", "foo", "index.js"), "")
	mustWrite(t, filepath.Join(dir, ".git", "HEAD"), "")

	m := New()
	m.Open(dir, 0)

	haveMain, havePkg := false, false
	for _, p := range m.allPaths {
		if p == "main.go" {
			haveMain = true
		}
		if p == filepath.Join("pkg", "util.go") {
			havePkg = true
		}
		if strings.Contains(p, "node_modules") {
			t.Errorf("node_modules shouldn't be scanned: %s", p)
		}
		if strings.Contains(p, ".git") {
			t.Errorf(".git shouldn't be scanned: %s", p)
		}
	}
	if !haveMain {
		t.Errorf("main.go missing from allPaths: %v", m.allPaths)
	}
	if !havePkg {
		t.Errorf("pkg/util.go missing from allPaths: %v", m.allPaths)
	}
}

// TestFuzzyQueryNarrowsMatches: SetQuery runs the fuzzy filter and
// reorders the match list. Typing "util" should surface pkg/util.go
// even when main.go is lexicographically first.
func TestFuzzyQueryNarrowsMatches(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "")
	mustWrite(t, filepath.Join(dir, "pkg", "util.go"), "")
	mustWrite(t, filepath.Join(dir, "README.md"), "")

	m := New()
	m.Open(dir, 0)
	m.SetQuery("util")

	if len(m.Matches) == 0 {
		t.Fatal("expected matches for 'util'")
	}
	if !strings.Contains(m.Matches[0], "util.go") {
		t.Errorf("top match should contain util.go: %v", m.Matches)
	}
}

func TestOpenWithItemsShowsAgentsBeforeFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "")

	m := New()
	m.OpenWithItems(dir, 0, []Item{
		{
			Kind:    KindAgent,
			ID:      "plan",
			Display: "Plan",
			Meta:    "read-only planning tools",
		},
		{
			Kind:    KindSession,
			ID:      "sess_1",
			Display: "Session one",
			Meta:    "session metadata",
		},
		{
			Kind:    KindSkill,
			ID:      "bugfix",
			Display: "bugfix",
			Meta:    "reproduce then fix",
		},
	})

	if len(m.Matches) < 4 {
		t.Fatalf("expected agent + session + skill + file matches, got %v", m.Matches)
	}
	if m.Matches[0] != "Plan" {
		t.Fatalf("first match = %q, want Plan", m.Matches[0])
	}
	item, ok := m.SelectedItem()
	if !ok || item.Kind != KindAgent || item.ID != "plan" {
		t.Fatalf("selected item = %+v, %v; want plan agent", item, ok)
	}
	if m.Matches[1] != "Session one" {
		t.Fatalf("second match = %q, want Session one", m.Matches[1])
	}
	if m.Matches[2] != "bugfix" {
		t.Fatalf("third match = %q, want bugfix", m.Matches[2])
	}
}

// TestUpDownNavigateHandled: arrow keys move the cursor and return
// handled=true so the host MUST NOT pass them through to the editor.
// Without this, Up/Down would scroll the textarea while the popover
// was open — exactly the wrong UX.
func TestUpDownNavigateHandled(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "")
	mustWrite(t, filepath.Join(dir, "b.go"), "")
	mustWrite(t, filepath.Join(dir, "c.go"), "")

	m := New()
	m.Open(dir, 0)

	_, handled := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !handled {
		t.Error("KeyDown must return handled=true while visible")
	}
	if m.Cursor != 1 {
		t.Errorf("Cursor = %d, want 1 after Down", m.Cursor)
	}

	_, handled = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !handled {
		t.Error("KeyUp must return handled=true")
	}
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0 after Up from 1", m.Cursor)
	}

	// Up at 0 wraps to last.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != len(m.Matches)-1 {
		t.Errorf("Up-wrap: Cursor = %d, want %d", m.Cursor, len(m.Matches)-1)
	}
}

// TestSelectedWhenEmptyMatches: no matches → Selected() returns "".
func TestSelectedWhenEmptyMatches(t *testing.T) {
	m := New()
	m.Open(t.TempDir(), 0)
	m.SetQuery("zzzzzznotafile")

	if sel := m.Selected(); sel != "" {
		t.Errorf("empty-match Selected() = %q, want \"\"", sel)
	}
}

// TestCloseClearsState: Close hides the picker and wipes cached paths
// so the next Open() re-scans from scratch (repos can change between
// invocations).
func TestCloseClearsState(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "")

	m := New()
	m.Open(dir, 5)
	m.Close()

	if m.Visible {
		t.Error("Visible should be false after Close")
	}
	if m.Anchor != 0 {
		t.Errorf("Anchor = %d, want 0 after Close", m.Anchor)
	}
	if len(m.allPaths) != 0 {
		t.Errorf("allPaths should be cleared after Close, got %d", len(m.allPaths))
	}
	if len(m.Matches) != 0 {
		t.Errorf("Matches should be cleared, got %d", len(m.Matches))
	}
}

// TestHiddenUpdateNoop: Update on a hidden picker must return handled=false
// so the host passes the event through normally.
func TestHiddenUpdateNoop(t *testing.T) {
	m := New()
	_, handled := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if handled {
		t.Error("hidden picker must not claim keypress")
	}
}

func TestOpen_SkipsControlCharFilenames(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "good.txt"), "")
	if err := os.WriteFile(filepath.Join(dir, "bad\x1bname.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New()
	m.Open(dir, 0)

	for _, p := range m.allPaths {
		if strings.ContainsRune(p, '\x1b') {
			t.Fatalf("control-char path leaked into picker: %q", p)
		}
	}
}

func TestView_StripsControlCharsFromRenderedRows(t *testing.T) {
	m := New()
	m.Visible = true
	m.matchedItems = []Item{
		{Kind: KindFile, Display: "safe.txt"},
		{Kind: KindFile, Display: "bad\x1bname.txt", Meta: "meta\x1bdata"},
	}
	m.refreshMatchStrings()
	out := m.View(80)
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("picker view leaked control chars: %q", out)
	}
	if !strings.Contains(out, "badname.txt") {
		t.Fatalf("sanitized filename missing: %q", out)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
