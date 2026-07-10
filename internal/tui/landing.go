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

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/changelog"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/version"
)

const (
	landingBannerMinHeight = 6
	// landingBannerMaxHeight bounds the banner-art assets (banner.txt is 26
	// rows, banner.ansi is 34 rows of chafa-rendered block art). The banner
	// is shown only when it fits at its natural height within the chat area;
	// otherwise it's replaced by the compact wordmark (never downsampled). The
	// asserts in viewport_test.go use this as the upper bound.
	landingBannerMaxHeight = 36
	// landingLogoMargin is the fixed number of blank lines between the logo
	// and the input box, so the gap reads the same whether the full sheep or
	// the compact wordmark is shown.
	landingLogoMargin = 2
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
	plugins := m.landingPluginsHint(width)
	providerHint := m.landingProviderHint(width)

	// #22: a brief changelog summary anchored to the upper-left corner. It
	// sits above the centered logo/input, so reserve its height from the body.
	// Skip it on short terminals so it can't crowd out the logo + input box.
	// Computed BEFORE the startup banner so its height is part of the banner's
	// reserve budget (P2.14): otherwise the banner over-budgets, the body
	// clamps to 1 row, and the whole stack overflows the terminal, pushing the
	// footer off the bottom.
	whatsNew := ""
	if height >= 20 {
		whatsNew = m.renderLandingWhatsNew(width)
	}
	whatsNewH := 0
	if whatsNew != "" {
		whatsNewH = lipgloss.Height(whatsNew) + 1 // +1 for the gap below it
	}

	// Startup banner (sandbox posture, broker session, writable paths)
	// rendered just above the footer so a fresh launch still surfaces what
	// the alt-screen cleared, without disturbing the centered welcome.
	// Width(width) wraps long lines (the unsandboxed warning is ~200 chars)
	// BEFORE we measure height — otherwise lipgloss.Height counts logical
	// lines, undercounts the wrapped rows, and the layout overflows. Tone
	// matches how the same block renders in scrollback (systemBlockTone).
	banner := ""
	if b := m.startupBannerText(); b != "" {
		// Wrap to width as PLAIN text first, so truncation operates on
		// real screen rows and never splits lipgloss's ANSI styling
		// (truncating a styled string can drop the trailing reset and
		// bleed colour into the footer).
		wrapped := lipgloss.NewStyle().Width(width).Render(b)
		// Cap the banner height so the input box AND the footer stay on
		// screen. The unsandboxed warning wraps to ~9-14 rows at 80 cols,
		// which on a 24-row terminal would push the footer off the bottom
		// (the banner is subtracted from bodyH below). Reserve room for the
		// whatsNew accent, a one-row compact logo + its margin, the input box
		// + hint (+ plugins), the input/hint gaps, the banner gap, and the
		// footer; truncate the overflow with a "(+N more)" marker pointing at
		// scrollback, where the full block also lives. Without whatsNewH in
		// this reserve the body clamps to 1 and the stack overflows (P2.14).
		reserved := whatsNewH + 1 + landingLogoMargin +
			lipgloss.Height(input) + lipgloss.Height(hint) + 2
		if plugins != "" {
			reserved += lipgloss.Height(plugins) + 1
		}
		if providerHint != "" {
			reserved += lipgloss.Height(providerHint) + 1
		}
		wrapped = truncateBanner(wrapped, height-reserved-1)
		// Style + re-wrap as one block: Width(width) also wraps the
		// marker line, so neither it nor any banner row overflows
		// horizontally, and the ANSI is self-contained.
		banner = lipgloss.NewStyle().
			Foreground(m.theme.Fg(systemBlockTone(b)).GetForeground()).
			Width(width).
			Render(wrapped)
	}

	// logoBudget computes how much vertical room the logo may use, given a
	// whatsNewH reservation. Hoisted so we can re-evaluate with whatsNew
	// dropped (see below): the changelog block is a corner accent, but the
	// banner is the primary brand element, so when both can't fit the banner
	// wins.
	logoBudget := func(whatsNewH int) int {
		bodyH := height - 1
		if banner != "" {
			bodyH -= lipgloss.Height(banner) + 1
		}
		bodyH -= whatsNewH
		if bodyH < 1 {
			bodyH = 1
		}
		// Reserve room for the input, hint, the fixed logo margin (blank
		// lines), and the input/hint gap (1 line) before deciding how much
		// height the logo may use.
		maxH := bodyH - lipgloss.Height(input) - lipgloss.Height(hint) - landingLogoMargin - 1
		if plugins != "" {
			maxH -= lipgloss.Height(plugins) + 1
		}
		if providerHint != "" {
			maxH -= lipgloss.Height(providerHint) + 1
		}
		if maxH < 0 {
			maxH = 0
		}
		return maxH
	}

	logo := renderLandingLogo(width, logoBudget(whatsNewH))
	// De-prioritize whatsNew vs the banner (P2.13): if the changelog accent
	// forced the logo down to the compact wordmark, but the full branded
	// banner WOULD render once whatsNew is dropped, drop it. This clears the
	// off-by-one at the common 50-row maximized terminal, where the 6-row
	// whatsNew block plus the startup banner shaved the logo budget just
	// below the banner's natural height.
	if whatsNew != "" && isCompactLogo(logo) {
		if full := renderLandingLogo(width, logoBudget(0)); !isCompactLogo(full) {
			whatsNew = ""
			whatsNewH = 0
			logo = full
		}
	}

	bodyH := height - 1
	if banner != "" {
		bodyH -= lipgloss.Height(banner) + 1
	}
	bodyH -= whatsNewH
	if bodyH < 1 {
		bodyH = 1
	}

	// Below-logo stack (input · provider-hint · plugins · hint), then the logo
	// prepended with a FIXED margin so the gap is identical whether the full
	// sheep or the compact wordmark renders.
	below := make([]string, 0, 4)
	below = append(below, centerLines(input, width))
	if providerHint != "" {
		below = append(below, centerLines(providerHint, width))
	}
	if plugins != "" {
		below = append(below, centerLines(plugins, width))
	}
	below = append(below, centerLines(hint, width))
	stack := strings.Join(below, "\n\n")
	if logo != "" {
		stack = logo + strings.Repeat("\n", landingLogoMargin+1) + stack
	}
	body := lipgloss.Place(width, bodyH, lipgloss.Center, lipgloss.Center, stack)
	out := ""
	if whatsNew != "" {
		out = whatsNew + "\n\n" // top-left, left-aligned (not centered)
	}
	out += body
	if banner != "" {
		out += "\n" + banner
	}
	return out + "\n" + m.renderLandingFooter(width)
}

// truncateBanner clips a width-wrapped (plain, unstyled) banner to at most
// maxH rows. When it overflows, the last kept row is replaced with a
// "… (+N more — see scrollback)" marker so the landing input box is never
// pushed off a short terminal. The full banner remains in scrollback as a
// system block, so nothing is lost. maxH < 1 clamps to 1 (caller always
// truncates — even a 1-row budget beats horizontal/vertical overflow); the
// caller re-applies width wrapping after, so the marker can't overflow.
func truncateBanner(banner string, maxH int) string {
	if maxH < 1 {
		maxH = 1
	}
	lines := strings.Split(banner, "\n")
	if len(lines) <= maxH {
		return banner
	}
	keep := lines[:maxH-1]
	hidden := len(lines) - len(keep)
	out := append([]string(nil), keep...)
	out = append(out, fmt.Sprintf("… (+%d more — see scrollback)", hidden))
	return strings.Join(out, "\n")
}

func renderLandingLogo(width, maxH int) string {
	raw := bannerFor(width)
	if raw == "" {
		return compactLandingLogo(width)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	// Never deform the sheep: render the banner only when it fits at its
	// natural height. If there isn't enough vertical room, fall back to the
	// compact "stado" wordmark rather than downsampling (which squashed the
	// art into an unrecognisable oval on short terminals).
	if maxH < landingBannerMinHeight || len(lines) > maxH {
		return compactLandingLogo(width)
	}
	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(lines, "\n")
}

func compactLandingLogo(width int) string {
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, "stado")
}

// isCompactLogo reports whether a rendered logo is the compact "stado"
// wordmark fallback rather than the full banner art. Used to decide whether
// the changelog accent should yield to the banner (P2.13): the wordmark is a
// single centered row whose only content is "stado".
func isCompactLogo(logo string) bool {
	if strings.Contains(logo, "\n") {
		return false
	}
	return strings.TrimSpace(logo) == "stado"
}

func landingHint(th *theme.Theme) string {
	if th == nil {
		return "ctrl+p commands"
	}
	return th.Fg("text_secondary").Bold(true).Render("ctrl+p") + " " +
		th.Fg("muted").Render("commands")
}

// landingNoProviderConfigured reports whether the session has no usable
// provider AND none is named in config — i.e. the very first turn will fail
// in ensureProvider. Returns false while the startup local-runner probe is
// still pending (the prewarm may yet pick ollama), so the hint doesn't flash
// during normal "no-config but a local runner is coming up" launches.
func (m *Model) landingNoProviderConfigured() bool {
	if m.provider != nil {
		return false
	}
	if m.providerProbePending {
		return false
	}
	return strings.TrimSpace(m.providerName) == ""
}

// landingProviderHint surfaces a concise "no provider configured" warning on
// the landing screen so a no-provider user learns it BEFORE submitting a
// message and hitting the ensureProvider failure (P3.8). Empty when a
// provider is configured/active or a local-runner probe is still pending.
func (m *Model) landingProviderHint(width int) string {
	if !m.landingNoProviderConfigured() {
		return ""
	}
	msg := "no provider configured — run `stado auth` or set defaults.provider"
	if m.theme == nil {
		return trimSeed(msg, width)
	}
	// trimSeed keeps the line within the landing width so it never wraps and
	// disturbs the centered layout; the full guidance is in the ensureProvider
	// failure block if the user submits anyway.
	return m.theme.Fg("warning").Render(trimSeed(msg, width))
}

// landingPluginsHint renders the autoloaded-plugin badge line under
// the input hint on the landing screen. Empty string when there is
// no executor (no registry to introspect) or zero autoloaded plugins
// — the caller skips the spacing block in that case. Q2 (low-prio
// follow-up to EP-no-internal-tools): the operator sees what plugin
// surface is live before typing the first prompt.
func (m *Model) landingPluginsHint(width int) string {
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
	return ansi.Truncate(count+strings.Join(parts, dot), width, "…")
}

// landingWhatsNewWidth caps the upper-left changelog block so it stays a
// corner accent and never crowds the centered logo.
func landingWhatsNewWidth(width int) int {
	w := width/3 + 8
	if w > 48 {
		w = 48
	}
	if w > width-2 {
		w = width - 2
	}
	return w
}

// renderLandingWhatsNew renders a brief summary of the latest CHANGELOG entry
// (version, headline, a few highlight lead-ins) for the landing screen's
// upper-left corner. Empty when the changelog can't be parsed or the terminal
// is too narrow to spare the room. (#22)
func (m *Model) renderLandingWhatsNew(width int) string {
	if m.theme == nil || width < 40 {
		return ""
	}
	rel := changelog.Latest()
	if rel.Version == "" {
		return ""
	}
	w := landingWhatsNewWidth(width)
	lines := []string{
		m.theme.Fg("text_secondary").Bold(true).Render("what's new") + " " +
			m.theme.Fg("accent").Render(rel.Version),
	}
	if rel.Title != "" {
		lines = append(lines, m.theme.Fg("muted").Render(trimSeed(rel.Title, w)))
	}
	for i, h := range rel.Highlights {
		if i >= 3 {
			break
		}
		lines = append(lines, m.theme.Fg("muted").Render("· "+trimSeed(h, w-2)))
	}
	return strings.Join(lines, "\n")
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
