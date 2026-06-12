package tui

// Cluster C3 — landing / onboarding render regressions. Each test
// reproduces a confirmed defect by RENDERING the real renderSidebar /
// renderLanding output at the cited geometry and asserting on the resulting
// line widths / presence of CTA, footer, banner, and hint.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// hasBannerArt reports whether a rendered (ansi-stripped) landing screen
// contains the branded sheep banner art rather than the compact "stado"
// wordmark — the banner uses unicode block glyphs.
func hasBannerArt(s string) bool {
	return strings.ContainsAny(s, "░▒▓█▂▃▅▆▔▀▖▗▘▝")
}

// stripTrailing strips ANSI then trailing spaces per line, returning the
// per-row display widths and the cleaned rows.
func renderedRows(t *testing.T, s string) []string {
	t.Helper()
	out := ansi.Strip(s)
	rows := strings.Split(out, "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return rows
}

// TestSidebar_ModelPlaceholderFitsContentWidth (P2.12): the no-model
// placeholder + provider line must fit the default 32-col sidebar's 28-col
// content width so the "/model" CTA isn't split across rows by hardWrap.
func TestSidebar_ModelPlaceholderFitsContentWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// A provider is set but no model — the launch state that previously
	// produced "no model set  —  /model via openrouter", which hardWrap
	// split as "...  /model vi" / "a openrouter".
	m := newPickerTestModel(t, "openrouter")
	m.model = ""
	m.width = 100

	got := ansi.Strip(m.renderSidebar(32)) // content width = 32 - 4 = 28
	rows := strings.Split(got, "\n")

	// The "/model" CTA must appear intact on a single row, not split.
	foundIntact := false
	for _, r := range rows {
		clean := strings.TrimRight(r, " ")
		if strings.Contains(clean, "/model") {
			foundIntact = true
		}
		// No row may exceed the 28-col content budget (display width).
		if w := ansi.StringWidth(clean); w > 28 {
			t.Errorf("sidebar row exceeds 28-col content width (%d): %q", w, clean)
		}
	}
	if !foundIntact {
		t.Fatalf("/model CTA not found intact on a single row\n%s", got)
	}
	// The CTA must not have been fragmented: the broken render left a
	// dangling "a openrouter" continuation row. Assert it's gone.
	for _, r := range rows {
		if strings.TrimSpace(r) == "a openrouter" {
			t.Fatalf("/model line was split across rows (found dangling %q)\n%s",
				strings.TrimSpace(r), got)
		}
	}
}

// TestModelOrPlaceholder_ShortEnoughForSidebar (P2.12 unit): the placeholder
// itself stays within the 28-col content budget and keeps "/model" intact.
func TestModelOrPlaceholder_ShortEnoughForSidebar(t *testing.T) {
	ph := modelOrPlaceholder("")
	if !strings.Contains(ph, "/model") {
		t.Fatalf("placeholder lost the /model CTA: %q", ph)
	}
	if w := len([]rune(ph)); w > 28 {
		t.Fatalf("placeholder %q is %d cols — wider than the 28-col sidebar content", ph, w)
	}
	// A real model name passes through unchanged.
	if got := modelOrPlaceholder("claude-opus-4-7"); got != "claude-opus-4-7" {
		t.Fatalf("modelOrPlaceholder mangled a real model: %q", got)
	}
}

// TestLanding_BrandedBannerAt50Rows (P2.13): at the common 50-row maximized
// terminal — with a realistic startup banner present, which is the normal
// launch case — the branded sheep banner must still render (not fall back to
// the tiny wordmark). The changelog "what's new" accent yields to the banner.
func TestLanding_BrandedBannerAt50Rows(t *testing.T) {
	// ANSI banner (34 rows) is the common case; NO_COLOR plain banner is
	// 26 rows and would fit trivially, so exercise the color path.
	t.Setenv("NO_COLOR", "")
	warn := "stado · sandbox: bwrap · session abc12345 · writable: /home/me/project"
	for _, w := range []int{120, 200} {
		m := newPickerTestModel(t, "anthropic")
		m.width, m.height = w, 50
		m.blocks = append(m.blocks, block{kind: "system", body: warn, startup: true})
		out := ansi.Strip(m.renderLanding(w, 50))
		if !hasBannerArt(out) {
			t.Errorf("w=%d h=50: branded banner missing — fell back to compact wordmark\n%s", w, out)
		}
		if rows := strings.Count(out, "\n") + 1; rows > 50 {
			t.Errorf("w=%d h=50: rendered %d rows — overflows", w, rows)
		}
	}
}

// TestLanding_WhatsNewYieldsThenReturns (P2.13 behavior): the changelog accent
// is dropped only when needed to fit the banner; once the terminal is tall
// enough for both, the accent returns. Guards against the priority inversion
// over-aggressively suppressing whatsNew.
func TestLanding_WhatsNewYieldsThenReturns(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	warn := "stado · sandbox: bwrap · session abc12345 · writable: /home/me/project"
	render := func(h int) string {
		m := newPickerTestModel(t, "anthropic")
		m.width, m.height = 200, h
		m.blocks = append(m.blocks, block{kind: "system", body: warn, startup: true})
		return ansi.Strip(m.renderLanding(200, h))
	}
	// At 50 rows: banner present, whatsNew yielded.
	at50 := render(50)
	if !hasBannerArt(at50) {
		t.Fatalf("h=50: banner should render\n%s", at50)
	}
	// With ample room (60 rows) both the banner and the accent render.
	at60 := render(60)
	if !hasBannerArt(at60) {
		t.Errorf("h=60: banner should render\n%s", at60)
	}
	if !strings.Contains(at60, "what's new") {
		t.Errorf("h=60: what's new accent should return when there is room\n%s", at60)
	}
}

// TestLanding_FooterSurvivesAt80x24 (P2.14): the version/cwd footer must stay
// on the last visible row at an 80x24 terminal even when the unsandboxed
// startup warning wraps to many rows — the warning is trimmed to its budget
// with the "(+N more)" marker rather than pushing the footer off-screen.
func TestLanding_FooterSurvivesAt80x24(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// The REAL unsandboxed warning: 3 long lines wrapping to ~9 rows at w80.
	warn := strings.Join([]string{
		"stado: warn: [sandbox] mode = \"external\" but no wrapper evidence detected for this entry point.",
		"stado: warn: only `stado run` validates external-mode wrapping today; TUI / session resume / headless do not.",
		"stado: warn: launch this entry point under your wrapper (bwrap/firejail/sandbox-exec/container), or set mode = \"wrap\" to have stado re-exec itself. Suppress with STADO_SUPPRESS_UNSANDBOXED_WARNING=1.",
	}, "\n")
	for _, h := range []int{22, 23, 24} {
		m := newPickerTestModel(t, "anthropic")
		m.width, m.height = 80, h
		m.blocks = append(m.blocks, block{kind: "system", body: warn, startup: true})
		rows := renderedRows(t, m.renderLanding(80, h))
		// Must not overflow the terminal height (would push footer below fold).
		if len(rows) > h {
			t.Errorf("h=%d: rendered %d rows — overflows, footer pushed off", h, len(rows))
		}
		// The footer (version) must be present on the LAST visible row.
		lastVisible := rows[min(h, len(rows))-1]
		if !strings.Contains(lastVisible, "0.0.0-dev") {
			t.Errorf("h=%d: footer not on last visible row %q\nall rows:\n%s",
				h, lastVisible, strings.Join(rows, "\n"))
		}
		// The trimmed warning must carry the "(+N more)" marker so the user
		// knows the rest is in scrollback.
		if !strings.Contains(strings.Join(rows, "\n"), "more") {
			t.Errorf("h=%d: expected a (+N more) truncation marker on the warning\n%s",
				h, strings.Join(rows, "\n"))
		}
	}
}

// TestLanding_NoProviderHint (P3.8): when no provider is configured, the
// landing screen surfaces a concise "no provider configured" hint so the user
// learns it BEFORE submitting a message. The hint is suppressed when a
// provider is configured or a local-runner probe is still pending.
func TestLanding_NoProviderHint(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	t.Run("shown when unconfigured", func(t *testing.T) {
		m := newPickerTestModel(t, "") // no provider name, no probe pending
		m.width, m.height = 120, 40
		out := strings.ToLower(ansi.Strip(m.renderLanding(120, 40)))
		if !strings.Contains(out, "no provider configured") {
			t.Fatalf("expected a no-provider hint on the landing screen\n%s", out)
		}
		if !strings.Contains(out, "stado auth") && !strings.Contains(out, "defaults.provider") {
			t.Errorf("no-provider hint lacks an actionable next step\n%s", out)
		}
	})

	t.Run("hidden when provider configured", func(t *testing.T) {
		m := newPickerTestModel(t, "anthropic")
		m.width, m.height = 120, 40
		out := strings.ToLower(ansi.Strip(m.renderLanding(120, 40)))
		if strings.Contains(out, "no provider configured") {
			t.Fatalf("no-provider hint should NOT show when a provider is set\n%s", out)
		}
	})

	t.Run("hidden while probing", func(t *testing.T) {
		m := newPickerTestModel(t, "")
		m.providerProbePending = true
		m.width, m.height = 120, 40
		out := strings.ToLower(ansi.Strip(m.renderLanding(120, 40)))
		if strings.Contains(out, "no provider configured") {
			t.Fatalf("no-provider hint should be suppressed while the local-runner probe is pending\n%s", out)
		}
	})
}
