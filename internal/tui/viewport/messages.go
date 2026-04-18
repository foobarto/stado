package viewport

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/theme"
)

type DisplayMsg struct {
	Role    string
	Content string
}

type Model struct {
	Viewport viewport.Model
	msgs     []DisplayMsg
	renderer *glamour.TermRenderer
	reg      *keys.Registry
}

func New(reg *keys.Registry, renderer *glamour.TermRenderer) *Model {
	vp := viewport.New(0, 0)
	return &Model{
		Viewport: vp,
		renderer: renderer,
		reg:      reg,
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	handled := false

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case m.reg.Matches(msg, keys.MessagesPageUp):
			m.Viewport.HalfViewUp()
			handled = true
		case m.reg.Matches(msg, keys.MessagesPageDown):
			m.Viewport.HalfViewDown()
			handled = true
		case m.reg.Matches(msg, keys.MessagesHalfPageUp):
			m.Viewport.LineUp(m.Viewport.Height / 2)
			handled = true
		case m.reg.Matches(msg, keys.MessagesHalfPageDown):
			m.Viewport.LineDown(m.Viewport.Height / 2)
			handled = true
		case m.reg.Matches(msg, keys.MessagesFirst):
			m.Viewport.GotoTop()
			handled = true
		case m.reg.Matches(msg, keys.MessagesLast):
			m.Viewport.GotoBottom()
			handled = true
		}
	}

	if !handled {
		m.Viewport, cmd = m.Viewport.Update(msg)
	}
	return cmd, handled
}

func (m *Model) SetSize(width, height int) {
	m.Viewport.Width = width
	m.Viewport.Height = height
}

func (m *Model) Append(msg DisplayMsg) {
	m.msgs = append(m.msgs, msg)
	m.render()
	m.Viewport.GotoBottom()
}

func (m *Model) AppendTextToLast(text string) {
	if len(m.msgs) > 0 && m.msgs[len(m.msgs)-1].Role == "assistant" {
		m.msgs[len(m.msgs)-1].Content += text
		m.render()
		m.Viewport.GotoBottom()
	} else {
		m.Append(DisplayMsg{Role: "assistant", Content: text})
	}
}

func (m *Model) Clear() {
	m.msgs = nil
	m.render()
	m.Viewport.GotoTop()
}

func (m *Model) render() {
	var b strings.Builder
	for _, dm := range m.msgs {
		switch dm.Role {
		case "user":
			b.WriteString(theme.MsgUser.Render("▸ "+dm.Content) + "\n")
		case "assistant":
			rendered, err := m.renderer.Render(dm.Content)
			if err != nil {
				rendered = dm.Content
			}
			b.WriteString(theme.MsgAI.Render(rendered) + "\n")
		case "tool_call":
			b.WriteString(theme.MsgTool.Render("⚙ "+dm.Content) + "\n")
		case "system":
			b.WriteString(theme.ErrorStyle.Render(dm.Content) + "\n")
		}
	}
	m.Viewport.SetContent(b.String())
}

func (m *Model) View() string {
	return m.Viewport.View()
}
