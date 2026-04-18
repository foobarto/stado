package theme

import "github.com/charmbracelet/lipgloss"

// Backwards-compatible package-level globals. These are sourced from the
// bundled default theme so older TUI subpackages (input, palette, overlays,
// viewport) keep working while the app migrates to explicit *Theme handles.
// New code should take a *Theme.
var (
	def = Default()

	Background  = def.color("background")
	Background2 = def.color("surface")
	Surface     = def.color("surface")
	Border      = def.color("border")
	Primary     = def.color("primary")
	Secondary   = def.color("accent")
	Success     = def.color("success")
	Warning     = def.color("warning")
	Error       = def.color("error")
	Muted       = def.color("muted")
	Text        = def.color("text")
	TextDim     = def.color("text_dim")
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
	App = lipgloss.NewStyle().Background(Background).Foreground(Text).Padding(0, 1)
	Header = lipgloss.NewStyle().Foreground(Primary).Bold(true).Padding(0, 1)
	StatusBar = lipgloss.NewStyle().Background(Background2).Foreground(TextDim).Padding(0, 1)
	InputBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Border).Padding(0, 1)
	MsgList = lipgloss.NewStyle().Padding(0, 1)
	MsgUser = lipgloss.NewStyle().Foreground(Primary).Bold(true).Padding(1, 0)
	MsgAI = lipgloss.NewStyle().Foreground(Text).Padding(1, 0)
	MsgTool = lipgloss.NewStyle().Foreground(Secondary).Padding(0, 1)
	StatusDot = lipgloss.NewStyle().Foreground(Success)
	Spinner = lipgloss.NewStyle().Foreground(Primary)
	ErrorStyle = lipgloss.NewStyle().Foreground(Error)
	BorderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Border)
	Title = lipgloss.NewStyle().Foreground(Primary).Bold(true)
}
