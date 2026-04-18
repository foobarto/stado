package theme

import "github.com/charmbracelet/lipgloss"

var (
	// Palette — OpenCode-faithful, own identity
	Background  = lipgloss.Color("#1a1b26")
	Background2 = lipgloss.Color("#16161e")
	Surface     = lipgloss.Color("#24283b")
	Border      = lipgloss.Color("#3b4261")
	Primary     = lipgloss.Color("#7aa2f7")
	Secondary   = lipgloss.Color("#bb9af7")
	Success     = lipgloss.Color("#9ece6a")
	Warning     = lipgloss.Color("#e0af68")
	Error       = lipgloss.Color("#f7768e")
	Muted       = lipgloss.Color("#565f89")
	Text        = lipgloss.Color("#c0caf5")
	TextDim     = lipgloss.Color("#565f89")
)

var (
	App         lipgloss.Style
	Header      lipgloss.Style
	StatusBar   lipgloss.Style
	InputBox    lipgloss.Style
	MsgList     lipgloss.Style
	MsgUser     lipgloss.Style
	MsgAI       lipgloss.Style
	MsgTool     lipgloss.Style
	StatusDot   lipgloss.Style
	Spinner     lipgloss.Style
	ErrorStyle  lipgloss.Style
	BorderStyle lipgloss.Style
	Title       lipgloss.Style
)

func init() {
	App = lipgloss.NewStyle().
		Background(Background).
		Foreground(Text).
		Padding(0, 1)

	Header = lipgloss.NewStyle().
		Foreground(Primary).
		Bold(true).
		Padding(0, 1)

	StatusBar = lipgloss.NewStyle().
		Background(Background2).
		Foreground(TextDim).
		Padding(0, 1)

	InputBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Border).
		Padding(0, 1)

	MsgList = lipgloss.NewStyle().
		Padding(0, 1)

	MsgUser = lipgloss.NewStyle().
		Foreground(Primary).
		Bold(true).
		Padding(1, 0)

	MsgAI = lipgloss.NewStyle().
		Foreground(Text).
		Padding(1, 0)

	MsgTool = lipgloss.NewStyle().
		Foreground(Secondary).
		Padding(0, 1)

	StatusDot = lipgloss.NewStyle().
		Foreground(Success)

	Spinner = lipgloss.NewStyle().
		Foreground(Primary)

	ErrorStyle = lipgloss.NewStyle().
		Foreground(Error)

	BorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Border)

	Title = lipgloss.NewStyle().
		Foreground(Primary).
		Bold(true)
}
