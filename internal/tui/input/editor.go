package input

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/theme"
)

type Editor struct {
	Model   textarea.Model
	History *History
	reg     *keys.Registry
}

func New(reg *keys.Registry) *Editor {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Enter to send, Shift+Enter for new line)"
	// No per-line prompt — opencode-style: the bordered pane is the frame,
	// and the textarea itself leaves the left margin clean. The mode
	// indicator in the inline status line below the text area conveys
	// "Plan/Do" without needing a gutter glyph.
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.ShowLineNumbers = false

	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(theme.Primary)
	ta.FocusedStyle.Text = lipgloss.NewStyle().Foreground(theme.Text)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(theme.Primary)
	ta.Cursor.TextStyle = lipgloss.NewStyle().Foreground(theme.Primary)

	ta.BlurredStyle = ta.FocusedStyle

	ta.Focus()

	ta.KeyMap.InsertNewline.SetKeys(keysToStrings(reg.Get(keys.InputNewline))...)
	ta.KeyMap.CharacterBackward.SetKeys(keysToStrings(reg.Get(keys.InputMoveLeft))...)
	ta.KeyMap.CharacterForward.SetKeys(keysToStrings(reg.Get(keys.InputMoveRight))...)
	ta.KeyMap.WordBackward.SetKeys(keysToStrings(reg.Get(keys.InputWordBackward))...)
	ta.KeyMap.WordForward.SetKeys(keysToStrings(reg.Get(keys.InputWordForward))...)
	ta.KeyMap.LineStart.SetKeys(keysToStrings(reg.Get(keys.InputLineHome))...)
	ta.KeyMap.LineEnd.SetKeys(keysToStrings(reg.Get(keys.InputLineEnd))...)
	ta.KeyMap.DeleteWordBackward.SetKeys(keysToStrings(reg.Get(keys.InputDeleteWordBackward))...)
	ta.KeyMap.DeleteWordForward.SetKeys(keysToStrings(reg.Get(keys.InputDeleteWordForward))...)
	ta.KeyMap.DeleteCharacterBackward.SetKeys(keysToStrings(reg.Get(keys.InputBackspace))...)
	ta.KeyMap.DeleteCharacterForward.SetKeys(keysToStrings(reg.Get(keys.InputDelete))...)
	
	// We want to handle Up/Down history ourselves without textarea swallowing them
	ta.KeyMap.LineNext.SetEnabled(false)
	ta.KeyMap.LinePrevious.SetEnabled(false)

	return &Editor{
		Model:   ta,
		History: NewHistory(),
		reg:     reg,
	}
}

func keysToStrings(bindings []key.Binding) []string {
	var out []string
	for _, b := range bindings {
		out = append(out, b.Keys()...)
	}
	return out
}

func (e *Editor) Update(msg tea.Msg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	handled := false

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case e.reg.Matches(msg, keys.InputSubmit):
			return nil, false

		case e.reg.Matches(msg, keys.InputClear):
			if e.Value() != "" {
				e.Model.Reset()
				handled = true
			}

		case e.reg.Matches(msg, keys.HistoryPrevious):
			if val, ok := e.History.Prev(e.Value()); ok {
				e.Model.SetValue(val)
				e.Model.CursorEnd()
			}
			handled = true

		case e.reg.Matches(msg, keys.HistoryNext):
			if val, ok := e.History.Next(); ok {
				e.Model.SetValue(val)
				e.Model.CursorEnd()
			}
			handled = true
		}
	}

	if !handled {
		e.Model, cmd = e.Model.Update(msg)
	}
	return cmd, handled
}

func (e *Editor) View() string {
	return e.Model.View()
}

func (e *Editor) Value() string {
	return e.Model.Value()
}

func (e *Editor) Reset() {
	e.Model.Reset()
	e.History.ResetIndex()
}

// SetValue replaces the editor contents and places the cursor at the end.
// Used to programmatically open the slash palette from Ctrl+P.
func (e *Editor) SetValue(s string) {
	e.Model.SetValue(s)
	e.Model.CursorEnd()
}
