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
	for _, p := range []string{"main.go", "pkg/util.go", "README.md", "docs/guide.md"} {
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

func TestFilePicker_AtTriggerShowsSkillsAfterAgents(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bugfix", "Reproduce first", "Reproduce the bug first, then fix.\n")
	m := newSkillModel(t, root)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	if !m.filePicker.Visible {
		t.Fatal("picker should be visible after typing '@'")
	}
	if len(m.filePicker.Matches) < 4 {
		t.Fatalf("expected agents plus skill, got %v", m.filePicker.Matches)
	}
	for i, want := range []string{"Do", "Plan", "BTW"} {
		if m.filePicker.Matches[i] != want {
			t.Fatalf("match %d = %q, want %q (all matches: %v)", i, m.filePicker.Matches[i], want, m.filePicker.Matches)
		}
	}
	if m.filePicker.Matches[3] != "bugfix" {
		t.Fatalf("first non-agent match = %q, want bugfix (all matches: %v)", m.filePicker.Matches[3], m.filePicker.Matches)
	}
}

func TestFilePicker_AtTriggerShowsDocsBeforeFiles(t *testing.T) {
	m, _ := filePickerModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	if !m.filePicker.Visible {
		t.Fatal("picker should be visible after typing '@'")
	}
	if len(m.filePicker.Matches) < 6 {
		t.Fatalf("expected agents plus docs plus files, got %v", m.filePicker.Matches)
	}
	for i, want := range []string{"Do", "Plan", "BTW"} {
		if m.filePicker.Matches[i] != want {
			t.Fatalf("match %d = %q, want %q (all matches: %v)", i, m.filePicker.Matches[i], want, m.filePicker.Matches)
		}
	}
	if m.filePicker.Matches[3] != "README.md" || m.filePicker.Matches[4] != "docs/guide.md" {
		t.Fatalf("docs should appear after agents and before files: %v", m.filePicker.Matches)
	}
}

func TestFilePicker_SkillAcceptInjectsPromptAndConsumesMention(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bugfix", "Reproduce first", "Reproduce the bug first, then fix.\n")
	m := newSkillModel(t, root)

	for _, r := range "@bugfix" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	item, ok := m.filePicker.SelectedItem()
	if !ok || item.Kind != "skill" || item.ID != "bugfix" {
		t.Fatalf("selected item = %+v, %v; want bugfix skill", item, ok)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if m.filePicker.Visible {
		t.Fatal("skill accept should close picker")
	}
	if m.input.Value() != "" {
		t.Fatalf("skill-only mention should be consumed, input=%q", m.input.Value())
	}
	if len(m.msgs) != 1 || m.msgs[0].Role != agent.RoleUser {
		t.Fatalf("skill should inject one user message, got %+v", m.msgs)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].body, "Reproduce the bug first") {
		t.Fatalf("skill body not rendered: %+v", m.blocks[len(m.blocks)-1])
	}
}

func TestFilePicker_SkillAcceptInsidePromptPreservesDraftText(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bugfix", "Reproduce first", "Reproduce the bug first, then fix.\n")
	m := newSkillModel(t, root)

	for _, r := range "please @bugfix" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if len(m.msgs) != 1 {
		t.Fatalf("skill should inject one message, got %d", len(m.msgs))
	}
	if got := m.input.Value(); got != "please " {
		t.Fatalf("draft after skill accept = %q, want %q", got, "please ")
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

func TestFilePicker_SymbolAcceptInsertsLocation(t *testing.T) {
	m, _ := filePickerModel(t)
	if err := os.WriteFile(filepath.Join(m.cwd, "symbols.go"), []byte("package x\n\nfunc Widget() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, r := range "@Widget" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	item, ok := m.filePicker.SelectedItem()
	if !ok || item.Kind != "symbol" || item.Display != "Widget" {
		t.Fatalf("selected item = %+v, %v; want Widget symbol", item, ok)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if got := m.input.Value(); !strings.Contains(got, "symbols.go:3") {
		t.Fatalf("symbol accept should insert file location, got %q", got)
	}
}

func TestFilePicker_PythonSymbolAcceptInsertsLocation(t *testing.T) {
	m, _ := filePickerModel(t)
	src := "class Widget:\n    pass\n\nasync def run_widget():\n    pass\n\ndef helper():\n    pass\n"
	if err := os.WriteFile(filepath.Join(m.cwd, "tools.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, r := range "@run_widget" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	item, ok := m.filePicker.SelectedItem()
	if !ok || item.Kind != "symbol" || item.Display != "run_widget" || !strings.Contains(item.Meta, "python func") {
		t.Fatalf("selected item = %+v, %v; want run_widget python symbol", item, ok)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if got := m.input.Value(); !strings.Contains(got, "tools.py:4") {
		t.Fatalf("python symbol accept should insert file location, got %q", got)
	}
}

func TestFilePicker_TypeScriptSymbolAcceptInsertsLocation(t *testing.T) {
	m, _ := filePickerModel(t)
	src := "export class Widget {}\n\nexport async function runWidget() {}\n\nconst helper = () => {}\n"
	if err := os.WriteFile(filepath.Join(m.cwd, "ui.ts"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, r := range "@runWidget" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	item, ok := m.filePicker.SelectedItem()
	if !ok || item.Kind != "symbol" || item.Display != "runWidget" || !strings.Contains(item.Meta, "ts func") {
		t.Fatalf("selected item = %+v, %v; want runWidget TypeScript symbol", item, ok)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if got := m.input.Value(); !strings.Contains(got, "ui.ts:3") {
		t.Fatalf("typescript symbol accept should insert file location, got %q", got)
	}
}

func TestScanScriptFileSymbolsSkipsIndentedDeclarations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested.ts")
	src := "export function outer() {\n  function inner() {}\n  const localValue = 1\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	symbols := scanScriptFileSymbols("nested.ts", path, 10)
	if len(symbols) != 1 || symbols[0].Name != "outer" {
		t.Fatalf("symbols = %+v, want only outer", symbols)
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

func writeSkill(t *testing.T, root, name, desc, body string) {
	t.Helper()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}
