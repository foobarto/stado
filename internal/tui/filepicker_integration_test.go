package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// filePickerModel returns a Model with a repo-ish tempdir wired as cwd
// and a couple of files for the picker to find.
func filePickerModel(t *testing.T) (*Model, string) {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"main.go", "pkg/util.go", "README.md"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel(dir, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.width, m.height = 120, 30
	return m, dir
}

// TestFilePicker_AtTriggerOpensPicker: typing '@' into the editor
// should open the picker with an empty query matching the repo's
// tracked files.
func TestFilePicker_AtTriggerOpensPicker(t *testing.T) {
	m, _ := filePickerModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	if !m.filePicker.Visible {
		t.Fatal("picker should be visible after typing '@'")
	}
	if m.filePicker.Anchor != 0 {
		t.Errorf("anchor = %d, want 0 (only '@' in buffer)", m.filePicker.Anchor)
	}
	if len(m.filePicker.Matches) == 0 {
		t.Error("expected matches after '@' on a populated cwd")
	}
}

func TestFilePicker_AtTriggerShowsAgentsFirst(t *testing.T) {
	m, _ := filePickerModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	if !m.filePicker.Visible {
		t.Fatal("picker should be visible after typing '@'")
	}
	if len(m.filePicker.Matches) < 4 {
		t.Fatalf("expected agents plus files, got %v", m.filePicker.Matches)
	}
	for i, want := range []string{"Do", "Plan", "BTW"} {
		if m.filePicker.Matches[i] != want {
			t.Fatalf("match %d = %q, want %q (all matches: %v)", i, m.filePicker.Matches[i], want, m.filePicker.Matches)
		}
	}
}

func TestFilePicker_AtTriggerShowsSessionsAfterAgents(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	if !m.filePicker.Visible {
		t.Fatal("picker should be visible after typing '@'")
	}
	if len(m.filePicker.Matches) < 5 {
		t.Fatalf("expected agents plus sessions, got %v", m.filePicker.Matches)
	}
	for i, want := range []string{"Do", "Plan", "BTW"} {
		if m.filePicker.Matches[i] != want {
			t.Fatalf("match %d = %q, want %q (all matches: %v)", i, m.filePicker.Matches[i], want, m.filePicker.Matches)
		}
	}
	foundSecond := false
	for _, match := range m.filePicker.Matches[3:] {
		if match == "second label" {
			foundSecond = true
			break
		}
	}
	if !foundSecond {
		t.Fatalf("session label missing after agents: %v", m.filePicker.Matches)
	}
}

func TestFilePicker_AgentAcceptSwitchesMode(t *testing.T) {
	m, _ := filePickerModel(t)

	for _, r := range "@plan" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.filePicker.Visible {
		t.Fatal("@plan should open picker")
	}
	if sel := m.filePicker.Selected(); sel != "Plan" {
		t.Fatalf("selected = %q, want Plan", sel)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if m.filePicker.Visible {
		t.Fatal("agent accept should close picker")
	}
	if m.mode != modePlan {
		t.Fatalf("mode = %s, want Plan", m.mode.String())
	}
	if strings.Contains(m.input.Value(), "@plan") {
		t.Fatalf("agent mention should be consumed, input=%q", m.input.Value())
	}
}

func TestFilePicker_SessionAcceptSwitchesWhenMentionIsDraft(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	for _, r := range "@second" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.filePicker.Visible {
		t.Fatal("@second should open picker")
	}
	item, ok := m.filePicker.SelectedItem()
	if !ok || item.Kind != "session" || item.ID != ids.second {
		t.Fatalf("selected item = %+v, %v; want second session", item, ok)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if m.filePicker.Visible {
		t.Fatal("session accept should close picker")
	}
	if m.session.ID != ids.second {
		t.Fatalf("session id = %s, want %s", m.session.ID, ids.second)
	}
	if m.input.Value() != "" {
		t.Fatalf("session switch should consume draft mention, input=%q", m.input.Value())
	}
	if len(m.msgs) != 2 {
		t.Fatalf("switched session should load persisted conversation, got %d messages", len(m.msgs))
	}
}

func TestFilePicker_SessionAcceptInsidePromptInsertsReference(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	for _, r := range "compare @second" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.filePicker.Visible {
		t.Fatal("@second should open picker")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if m.filePicker.Visible {
		t.Fatal("session accept should close picker")
	}
	if m.session.ID != ids.first {
		t.Fatalf("mixed-prompt session mention should not switch, got %s", m.session.ID)
	}
	val := m.input.Value()
	if strings.Contains(val, "@second") {
		t.Fatalf("mention fragment should be replaced, input=%q", val)
	}
	if !strings.Contains(val, "session:"+ids.second) {
		t.Fatalf("session reference missing, input=%q", val)
	}
}

// TestFilePicker_NarrowsAsYouType: typing '@util' should filter the
// matches to something containing 'util'.
func TestFilePicker_NarrowsAsYouType(t *testing.T) {
	m, _ := filePickerModel(t)

	for _, r := range []rune{'@', 'u', 't', 'i', 'l'} {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if !m.filePicker.Visible {
		t.Fatal("picker should be visible during @-fragment typing")
	}
	if len(m.filePicker.Matches) == 0 {
		t.Fatal("expected matches for 'util'")
	}
	found := false
	for _, p := range m.filePicker.Matches {
		if strings.Contains(p, "util.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("util.go should rank in @util matches: %v", m.filePicker.Matches)
	}
}

// TestFilePicker_TabAcceptsSelection: pressing Tab while the picker
// has a selection must replace the @-fragment with the path.
func TestFilePicker_TabAcceptsSelection(t *testing.T) {
	m, _ := filePickerModel(t)

	for _, r := range []rune{'@', 'u', 't', 'i', 'l'} {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.filePicker.Selected() == "" {
		t.Fatalf("no selection to accept: matches=%v", m.filePicker.Matches)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if m.filePicker.Visible {
		t.Error("Tab accept should close the picker")
	}
	val := m.input.Value()
	if strings.Contains(val, "@util") {
		t.Errorf("input should no longer contain '@util': %q", val)
	}
	if !strings.Contains(val, "util.go") {
		t.Errorf("input should contain the accepted path: %q", val)
	}
	if !strings.HasSuffix(val, " ") {
		t.Errorf("accept should append a trailing space for continued typing: %q", val)
	}
}

// TestFilePicker_SpaceClosesPicker: typing a space after the @-word
// means the user's done mentioning — picker should hide.
func TestFilePicker_SpaceClosesPicker(t *testing.T) {
	m, _ := filePickerModel(t)

	for _, r := range []rune{'@', 'm', 'a', 'i', 'n'} {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.filePicker.Visible {
		t.Fatal("picker should be visible after @main")
	}
	// Space as a literal rune — bubbletea's textarea.Model consumes
	// space via KeyRunes, not tea.KeySpace.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if m.filePicker.Visible {
		t.Errorf("picker should close once user types past the @-word; input=%q",
			m.input.Value())
	}
}

// TestFilePicker_EscCloses: pressing Escape dismisses the picker
// without changing the input.
func TestFilePicker_EscCloses(t *testing.T) {
	m, _ := filePickerModel(t)

	for _, r := range []rune{'@', 'f'} {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	beforeVal := m.input.Value()
	if !m.filePicker.Visible {
		t.Fatal("picker should be visible")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if m.filePicker.Visible {
		t.Error("Esc should close the picker")
	}
	if m.input.Value() != beforeVal {
		t.Errorf("Esc mutated input: %q → %q", beforeVal, m.input.Value())
	}
}

// TestFilePicker_EmailAtDoesNotTrigger: an `@` that immediately
// follows a non-space character (e.g. 'user@example') must not fire
// the picker — would be false-positive noise on email addresses.
func TestFilePicker_EmailAtDoesNotTrigger(t *testing.T) {
	m, _ := filePickerModel(t)

	for _, r := range []rune{'u', 's', 'e', 'r', '@'} {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.filePicker.Visible {
		t.Errorf("email-style @ should not trigger picker; input=%q", m.input.Value())
	}
}
