package tui

// Landing screen — what the user sees before sending the first turn.
// Renders the stado banner (or a compact "stado" wordmark on small
// terminals), the input box, an autoloaded-plugins hint, a key-binding
// hint ("ctrl+p commands"), and a footer with cwd + version. Once a
// turn fires, View() switches to the conversation layout and these
// helpers go unused for the rest of the session.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/version"
)

const (
	landingBannerMinHeight = 6
	// landingBannerMaxHeight is sized to comfortably fit BOTH banner
	// asset variants (banner.txt is 26 rows, banner.ansi is 34 rows
	// of chafa-rendered block art). The previous value of 8 forced
	// sampleLandingLogoLines to downsample to ~3:1, vertically
	// squashing the sheep into an unrecognisable oval. With this
	// ceiling, sampling only kicks in when the chat area is
	// genuinely small; on typical terminals the asset renders at its
	// natural aspect.
	landingBannerMaxHeight = 36
)

func landingInputWidth(width int) int {
	if width < 1 {
		return 1
	}
	target := 64
	if width < 90 {
		target = width - 8
	}
	if target > width-8 {
		target = width - 8
	}
	if target < 40 {
		target = width - 4
	}
	if target < 20 {
		target = width
	}
	if target < 1 {
		target = 1
	}
	return target
}

// landingInputW is the landing input-card width. While the inline `/` command
// palette is open it widens (with a small margin) so command descriptions
// render untruncated — the compact 64-col landing card otherwise clips them.
// The card returns to its compact width once the palette closes.
func (m *Model) landingInputW(width int) int {
	base := landingInputWidth(width)
	if m.slash.Visible && m.slashInline {
		if wide := width - 8; wide > base {
			return wide
		}
	}
	return base
}

func (m *Model) renderLanding(width, height int) string {
	if width < 1 {
		return ""
	}
	input := strings.TrimRight(m.renderInputBox(m.landingInputW(width)), "\n")
	hint := landingHint(m.theme)
	plugins := m.landingPluginsHint()

	// Startup banner (sandbox posture, broker session, writable paths)
	// rendered just above the footer so a fresh launch still surfaces what
	// the alt-screen cleared, without disturbing the centered welcome.
	// Width(width) wraps long lines (the unsandboxed warning is ~200 chars)
	// BEFORE we measure height — otherwise lipgloss.Height counts logical
	// lines, undercounts the wrapped rows, and the layout overflows. Tone
	// matches how the same block renders in scrollback (systemBlockTone).
	banner := ""
	if b := m.startupBannerText(); b != "" {
		banner = lipgloss.NewStyle().
			Foreground(m.theme.Fg(systemBlockTone(b)).GetForeground()).
			Width(width).
			Render(b)
	}

	bodyH := height - 1
	if banner != "" {
		bodyH -= lipgloss.Height(banner) + 1
	}
	if bodyH < 1 {
		bodyH = 1
	}
	logoMaxH := bodyH - lipgloss.Height(input) - lipgloss.Height(hint) - 3
	if plugins != "" {
		logoMaxH -= lipgloss.Height(plugins) + 1
	}
	if logoMaxH < 0 {
		logoMaxH = 0
	}
	logo := renderLandingLogo(width, logoMaxH)

	parts := make([]string, 0, 4)
	if logo != "" {
		parts = append(parts, logo)
	}
	parts = append(parts, centerLines(input, width))
	if plugins != "" {
		parts = append(parts, centerLines(plugins, width))
	}
	parts = append(parts, centerLines(hint, width))
	stack := strings.Join(parts, "\n\n")
	body := lipgloss.Place(width, bodyH, lipgloss.Center, lipgloss.Center, stack)
	out := body
	if banner != "" {
		out += "\n" + banner
	}
	return out + "\n" + m.renderLandingFooter(width)
}

func renderLandingLogo(width, maxH int) string {
	if maxH < landingBannerMinHeight {
		return compactLandingLogo(width)
	}
	raw := bannerFor(width)
	if raw == "" {
		return compactLandingLogo(width)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	targetH := maxH
	if targetH > landingBannerMaxHeight {
		targetH = landingBannerMaxHeight
	}
	lines = sampleLandingLogoLines(lines, targetH)
	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(lines, "\n")
}

func compactLandingLogo(width int) string {
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, "stado")
}

func sampleLandingLogoLines(lines []string, target int) []string {
	if target <= 0 || len(lines) <= target {
		return lines
	}
	if target == 1 {
		return lines[:1]
	}
	out := make([]string, 0, target)
	last := len(lines) - 1
	denom := target - 1
	for i := 0; i < target; i++ {
		idx := (i*last + denom/2) / denom
		out = append(out, lines[idx])
	}
	return out
}

func landingHint(th *theme.Theme) string {
	if th == nil {
		return "ctrl+p commands"
	}
	return th.Fg("text_secondary").Bold(true).Render("ctrl+p") + " " +
		th.Fg("muted").Render("commands")
}

// landingPluginsHint renders the autoloaded-plugin badge line under
// the input hint on the landing screen. Empty string when there is
// no executor (no registry to introspect) or zero autoloaded plugins
// — the caller skips the spacing block in that case. Q2 (low-prio
// follow-up to EP-no-internal-tools): the operator sees what plugin
// surface is live before typing the first prompt.
func (m *Model) landingPluginsHint() string {
	if m.executor == nil {
		return ""
	}
	names := runtime.AutoloadedPluginNames(m.executor.Registry, m.cfg)
	if len(names) == 0 {
		return ""
	}
	if m.theme == nil {
		return strings.Join(names, " · ")
	}
	dot := m.theme.Fg("muted").Render(" · ")
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = m.theme.Fg("text_secondary").Render(n)
	}
	count := m.theme.Fg("muted").Render(fmt.Sprintf("%d plugins  ", len(names)))
	return count + strings.Join(parts, dot)
}

func centerLines(s string, width int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderLandingFooter(width int) string {
	if width < 1 {
		return ""
	}
	left := m.compactLandingCwd(width)
	right := version.Version
	if right == "" {
		right = "0.0.0-dev"
	}
	left = m.theme.Fg("muted").Render(left)
	right = m.theme.Fg("muted").Render(right)
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func (m *Model) compactLandingCwd(width int) string {
	cwd := filepath.Clean(m.cwd)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, ok := strings.CutPrefix(cwd, home); ok {
			cwd = "~" + rel
		}
	}
	maxW := width - len(version.Version) - 4
	if maxW < 12 {
		maxW = 12
	}
	return trimSeed(cwd, maxW)
}
