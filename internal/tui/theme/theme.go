// Package theme defines stado's TUI theme as a TOML-loadable palette.
//
// The bundled default lives in default.toml (go:embed) and is overridable via
// ~/.config/stado/theme.toml or a path passed to Load.
package theme

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/pelletier/go-toml"
)

//go:embed default.toml
var defaultTOML []byte

// Theme is the loaded palette + named style cache.
type Theme struct {
	Name   string `toml:"name"`
	Colors Colors `toml:"colors"`
	Layout Layout `toml:"layout"`

	styles map[string]lipgloss.Style
}

// Colors are the raw palette values (hex or ANSI names). All fields are strings
// so users can write `primary = "#7aa2f7"` or `primary = "blue"` in their TOML.
type Colors struct {
	Background string `toml:"background"`
	Surface    string `toml:"surface"`
	Border     string `toml:"border"`
	Primary    string `toml:"primary"`
	Accent     string `toml:"accent"`
	Muted      string `toml:"muted"`
	Success    string `toml:"success"`
	Warning    string `toml:"warning"`
	Error      string `toml:"error"`

	Text          string `toml:"text"`
	TextDim       string `toml:"text_dim"`
	TextSecondary string `toml:"text_secondary"`

	RoleUser      string `toml:"role_user"`
	RoleAssistant string `toml:"role_assistant"`
	RoleThinking  string `toml:"role_thinking"`
	RoleTool      string `toml:"role_tool"`
	RoleSystem    string `toml:"role_system"`
}

// Layout knobs — things users commonly want to tweak without editing templates.
type Layout struct {
	SidebarWidth    int    `toml:"sidebar_width"`     // default 28
	SidebarMinWidth int    `toml:"sidebar_min_width"` // collapse below this
	BorderStyle     string `toml:"border_style"`      // "rounded" | "normal" | "thick" | "double"
	Padding         int    `toml:"padding"`           // cells of side-padding in main column
	MessageIndent   int    `toml:"message_indent"`    // gutter width for role marker
}

// Default returns the bundled theme.
func Default() *Theme {
	t, err := parse(defaultTOML)
	if err != nil {
		panic("theme: default toml malformed: " + err.Error())
	}
	return t
}

// Load reads a TOML theme file. Missing fields fall back to the bundled default.
func Load(path string) (*Theme, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("theme: read %s: %w", path, err)
	}
	// Merge: start from defaults, overlay loaded values.
	base := Default()
	overlay, err := parse(data)
	if err != nil {
		return nil, err
	}
	merge(&base.Colors, overlay.Colors)
	mergeLayout(&base.Layout, overlay.Layout)
	if overlay.Name != "" {
		base.Name = overlay.Name
	}
	base.styles = nil
	base.init()
	return base, nil
}

func parse(data []byte) (*Theme, error) {
	var t Theme
	if err := toml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("theme: parse: %w", err)
	}
	t.init()
	return &t, nil
}

func (t *Theme) init() {
	if t.Layout.SidebarWidth == 0 {
		t.Layout.SidebarWidth = 32
	}
	if t.Layout.BorderStyle == "" {
		t.Layout.BorderStyle = "rounded"
	}
	if t.Layout.MessageIndent == 0 {
		t.Layout.MessageIndent = 2
	}
	t.styles = map[string]lipgloss.Style{}
}

// color resolves a palette name ("primary", "role_user", …) to a lipgloss.Color
// string. Unknown names return the raw string, so users can pass hex directly.
func (t *Theme) color(name string) lipgloss.Color {
	switch name {
	case "background":
		return lipgloss.Color(t.Colors.Background)
	case "surface":
		return lipgloss.Color(t.Colors.Surface)
	case "border":
		return lipgloss.Color(t.Colors.Border)
	case "primary":
		return lipgloss.Color(t.Colors.Primary)
	case "accent":
		return lipgloss.Color(t.Colors.Accent)
	case "muted":
		return lipgloss.Color(t.Colors.Muted)
	case "success":
		return lipgloss.Color(t.Colors.Success)
	case "warning":
		return lipgloss.Color(t.Colors.Warning)
	case "error":
		return lipgloss.Color(t.Colors.Error)
	case "text":
		return lipgloss.Color(t.Colors.Text)
	case "text_dim":
		return lipgloss.Color(t.Colors.TextDim)
	case "text_secondary":
		return lipgloss.Color(t.Colors.TextSecondary)
	case "role_user":
		return lipgloss.Color(t.Colors.RoleUser)
	case "role_assistant":
		return lipgloss.Color(t.Colors.RoleAssistant)
	case "role_thinking":
		return lipgloss.Color(t.Colors.RoleThinking)
	case "role_tool":
		return lipgloss.Color(t.Colors.RoleTool)
	case "role_system":
		return lipgloss.Color(t.Colors.RoleSystem)
	}
	return lipgloss.Color(name) // raw passthrough
}

// Fg returns a lipgloss.Style with the named foreground colour.
func (t *Theme) Fg(name string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.color(name))
}

// Bg returns a lipgloss.Style with the named background colour.
func (t *Theme) Bg(name string) lipgloss.Style {
	return lipgloss.NewStyle().Background(t.color(name))
}

// Border returns the configured border style primitive.
func (t *Theme) Border() lipgloss.Border {
	switch t.Layout.BorderStyle {
	case "normal":
		return lipgloss.NormalBorder()
	case "thick":
		return lipgloss.ThickBorder()
	case "double":
		return lipgloss.DoubleBorder()
	case "hidden":
		return lipgloss.HiddenBorder()
	default:
		return lipgloss.RoundedBorder()
	}
}

// Pane returns a boxed pane style using the theme's border colour.
func (t *Theme) Pane() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(t.color("surface")).
		Border(t.Border()).
		BorderForeground(t.color("border")).
		Padding(0, 1)
}

// RoleColor returns the foreground colour for a given role name.
func (t *Theme) RoleColor(role string) lipgloss.Color {
	switch role {
	case "user":
		return t.color("role_user")
	case "assistant":
		return t.color("role_assistant")
	case "thinking":
		return t.color("role_thinking")
	case "tool":
		return t.color("role_tool")
	default:
		return t.color("role_system")
	}
}

func merge(dst *Colors, src Colors) {
	fields := []struct {
		dst *string
		src string
	}{
		{&dst.Background, src.Background}, {&dst.Surface, src.Surface}, {&dst.Border, src.Border},
		{&dst.Primary, src.Primary}, {&dst.Accent, src.Accent}, {&dst.Muted, src.Muted},
		{&dst.Success, src.Success}, {&dst.Warning, src.Warning}, {&dst.Error, src.Error},
		{&dst.Text, src.Text}, {&dst.TextDim, src.TextDim}, {&dst.TextSecondary, src.TextSecondary},
		{&dst.RoleUser, src.RoleUser}, {&dst.RoleAssistant, src.RoleAssistant},
		{&dst.RoleThinking, src.RoleThinking}, {&dst.RoleTool, src.RoleTool}, {&dst.RoleSystem, src.RoleSystem},
	}
	for _, f := range fields {
		if f.src != "" {
			*f.dst = f.src
		}
	}
}

func mergeLayout(dst *Layout, src Layout) {
	if src.SidebarWidth != 0 {
		dst.SidebarWidth = src.SidebarWidth
	}
	if src.SidebarMinWidth != 0 {
		dst.SidebarMinWidth = src.SidebarMinWidth
	}
	if src.BorderStyle != "" {
		dst.BorderStyle = src.BorderStyle
	}
	if src.Padding != 0 {
		dst.Padding = src.Padding
	}
	if src.MessageIndent != 0 {
		dst.MessageIndent = src.MessageIndent
	}
}
