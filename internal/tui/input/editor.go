package input

import (
	"unicode/utf8"

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

const (
	ExtraVisibleRows   = 3
	DefaultVisibleRows = 1 + ExtraVisibleRows
	MaxValueBytes      = 1 << 20
)

func New(reg *keys.Registry) *Editor {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Enter to send, Shift+Enter for new line)"
	// No per-line prompt — opencode-style: the bordered pane is the frame,
	// and the textarea itself leaves the left margin clean. The mode
	// indicator in the inline status line below the text area conveys
	// "Plan/Do" without needing a gutter glyph.
	ta.Prompt = ""
	ta.CharLimit = MaxValueBytes
	ta.ShowLineNumbers = false

	applyThemeToTextArea(&ta)

	ta.BlurredStyle = ta.FocusedStyle

	ta.Focus()
	// The model layout keeps this in sync with content height, but set
	// the default here too so standalone editor tests and the first
	// render agree.
	ta.SetHeight(DefaultVisibleRows)

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

// ApplyTheme refreshes editor styles after theme.Apply has updated the
// package-level theme colors.
func (e *Editor) ApplyTheme() {
	applyThemeToTextArea(&e.Model)
	e.Model.BlurredStyle = e.Model.FocusedStyle
}

func applyThemeToTextArea(ta *textarea.Model) {
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(theme.Primary)
	ta.FocusedStyle.Text = lipgloss.NewStyle().Foreground(theme.Text)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(theme.Primary)
	ta.Cursor.TextStyle = lipgloss.NewStyle().Foreground(theme.Primary)
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
				e.SetValue(val)
			}
			handled = true

		case e.reg.Matches(msg, keys.HistoryNext):
			if val, ok := e.History.Next(); ok {
				e.SetValue(val)
			}
			handled = true
		}

		if !handled && msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			remaining := MaxValueBytes - len(e.Model.Value())
			if remaining <= 0 {
				return nil, true
			}
			runes := runesWithinBytes(msg.Runes, remaining)
			if len(runes) < len(msg.Runes) {
				if len(runes) == 0 {
					return nil, true
				}
				msg.Runes = runes
				e.Model, cmd = e.Model.Update(msg)
				e.enforceByteLimit()
				return cmd, true
			}
		}
	}

	if !handled {
		e.Model, cmd = e.Model.Update(msg)
	}
	e.enforceByteLimit()
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
func (e *Editor) SetValue(s string) {
	e.Model.SetValue(truncateStringBytes(s, MaxValueBytes))
	e.Model.CursorEnd()
}

// CursorOffset returns the absolute byte offset of the text cursor in
// Value(). Sums the lengths of lines above the current row plus the
// column offset within the current line. Used by the file picker to
// find the @-trigger fragment the user is typing.
func (e *Editor) CursorOffset() int {
	val := e.Model.Value()
	line := e.Model.Line()
	col := e.Model.LineInfo().ColumnOffset
	if line <= 0 {
		if col > len(val) {
			return len(val)
		}
		return col
	}
	off := 0
	rows := 0
	for i := 0; i < len(val) && rows < line; i++ {
		off++
		if val[i] == '\n' {
			rows++
		}
	}
	off += col
	if off > len(val) {
		off = len(val)
	}
	return off
}

func (e *Editor) enforceByteLimit() {
	if len(e.Model.Value()) <= MaxValueBytes {
		return
	}
	e.SetValue(e.Model.Value())
}

func runesWithinBytes(runes []rune, maxBytes int) []rune {
	if maxBytes <= 0 {
		return nil
	}
	used := 0
	for i, r := range runes {
		n := utf8.RuneLen(r)
		if n < 0 {
			n = len(string(r))
		}
		if used+n > maxBytes {
			return runes[:i]
		}
		used += n
	}
	return runes
}

func truncateStringBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := 0
	for i := range s {
		if i > maxBytes {
			return s[:end]
		}
		end = i
	}
	return s[:end]
}
