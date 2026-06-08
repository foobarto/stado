package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// injectStartupNotices must surface the launch banner as a system block so
// the alt-screen TUI doesn't lose what the CLI entry points print to
// stderr before the program takes the screen. Regression test for the
// "startup messages disappear once the TUI appears" report.
func TestInjectStartupNotices_rendersSystemBlock(t *testing.T) {
	m := &Model{}
	notices := []string{
		"stado: warn: running without a process-containment sandbox.",
		"stado: sandbox=no-sandbox session=abc123 (broker-mediated)",
		"stado: writable: <cwd>, /tmp",
	}
	m.injectStartupNotices(notices)

	if len(m.blocks) != 1 {
		t.Fatalf("expected exactly 1 block, got %d", len(m.blocks))
	}
	if m.blocks[0].kind != "system" {
		t.Errorf("expected a system block, got kind=%q", m.blocks[0].kind)
	}
	body := m.lastSystemBlockBody()
	for _, want := range notices {
		if !strings.Contains(body, want) {
			t.Errorf("startup block missing line %q; body=%q", want, body)
		}
	}
	// All lines collapse into one block joined by newlines, not one block
	// per line.
	if !strings.Contains(body, "\n") {
		t.Errorf("expected multi-line body, got single line: %q", body)
	}
}

// Empty notices → no block. A sandboxed or suppressed launch must not get a
// stray empty system block at the top of the conversation.
func TestInjectStartupNotices_emptyIsNoOp(t *testing.T) {
	m := &Model{}
	m.injectStartupNotices(nil)
	m.injectStartupNotices([]string{})
	if len(m.blocks) != 0 {
		t.Errorf("expected no blocks for empty notices, got %d", len(m.blocks))
	}
}

// The banner lands after any already-present (replayed) blocks — a resumed
// session shows prior history first, then this launch's notice.
func TestInjectStartupNotices_appendsAfterExistingBlocks(t *testing.T) {
	m := &Model{}
	m.appendBlock(block{kind: "user", body: "earlier message"})
	m.injectStartupNotices([]string{"stado: warn: running without a process-containment sandbox."})
	if len(m.blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(m.blocks))
	}
	if m.blocks[0].kind != "user" || m.blocks[1].kind != "system" {
		t.Errorf("expected [user, system] order, got [%s, %s]", m.blocks[0].kind, m.blocks[1].kind)
	}
}

// Regression (Codex review on #78): the startup banner must NOT suppress
// the empty-session landing screen. View() decides landing via
// hasRealBlocks, which ignores startup-marked blocks; if it counted them,
// every fresh launch would lose the welcome screen (logo, hints, version).
func TestStartupBanner_DoesNotCountAsRealBlock(t *testing.T) {
	m := &Model{}
	m.injectStartupNotices([]string{"stado: warn: running without a process-containment sandbox."})
	if m.hasRealBlocks() {
		t.Error("startup banner alone must not be a real block (would hide the landing screen)")
	}
	if got := m.startupBannerText(); !strings.Contains(got, "process-containment") {
		t.Errorf("startupBannerText missing banner; got %q", got)
	}
	// A real conversation block flips it → conversation view takes over.
	m.appendBlock(block{kind: "user", body: "hello"})
	if !m.hasRealBlocks() {
		t.Error("a real user block must make hasRealBlocks true")
	}
}

// The landing screen must render the banner (above its footer) so a fresh
// launch surfaces the notices the alt-screen cleared.
func TestStartupBanner_RendersOnLandingScreen(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.injectStartupNotices([]string{"stado: sandbox=no-sandbox session=deadbeef (broker-mediated)"})
	out := m.renderLanding(80, 30)
	for _, want := range []string{"no-sandbox", "deadbeef", "broker-mediated"} {
		if !strings.Contains(out, want) {
			t.Errorf("landing screen missing startup banner token %q", want)
		}
	}
}

// Regression: a banner line WIDER than the terminal must be wrapped, so no
// rendered line exceeds the width (which would overflow/misalign the screen)
// and the height arithmetic stays honest. Before the Width(width) fix the
// ~200-char warning line was emitted unwrapped. Asserting per-line width is
// the real guard — lipgloss.Height counts logical lines, not wrapped rows, so
// a height check alone passes even with the bug.
func TestStartupBanner_LandingWrapsWideBanner(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	longLine := "stado: warn: install a wrapper (bwrap/firejail on Linux, sandbox-exec on macOS) " +
		"and set [sandbox] mode = \"wrap\" in config.toml; today only `stado run` re-execs. " +
		"Suppress with STADO_SUPPRESS_SANDBOX_WARN=1."
	m.injectStartupNotices([]string{
		"stado: warn: running without a process-containment sandbox.",
		longLine,
	})
	const w, h = 80, 30
	out := m.renderLanding(w, h)
	for i, line := range strings.Split(out, "\n") {
		if lw := lipgloss.Width(line); lw > w {
			t.Errorf("landing line %d width %d exceeds terminal width %d (unwrapped banner): %q", i, lw, w, line)
		}
	}
	// Content must survive the wrap, not be dropped/truncated.
	if !strings.Contains(out, "install a wrapper") || !strings.Contains(out, "SUPPRESS_SANDBOX_WARN") {
		t.Errorf("wrapped banner dropped content; out=%q", out)
	}
}

// Regression (TUI E2E LandingReflow @ 80x24): on a short terminal, a tall
// unsandboxed-warning banner must NOT push the input box off-screen. The
// banner is capped (with a "+N more" marker) so the input placeholder and
// hint stay visible and the rendered output fits the height budget. Found
// by the pty-bridge E2E suite; the banner-in-band work (v0.58.1) regressed
// it on 24-row terminals.
func TestStartupBanner_ShortTerminalKeepsInputVisible(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// The real unsandboxed banner: several long lines that each wrap to
	// 2+ rows at 80 cols, ~14 rows total — more than half of a 24-row
	// terminal.
	m.injectStartupNotices([]string{
		"stado: warn: running without a process-containment sandbox.",
		"stado: warn: host subprocesses (shell, plugin runners, LSP, daemon, hooks) inherit the host's filesystem and network access.",
		"stado: warn: install a wrapper (bwrap/firejail on Linux, sandbox-exec on macOS) and set [sandbox] mode = \"wrap\" in config.toml; today only `stado run` re-execs. Suppress with STADO_SUPPRESS_SANDBOX_WARN=1.",
		"stado: sandbox=default session=cd2223241e25471f20b67ee745481439 (broker-mediated)",
		"stado: writable: /home/foobarto/Dokumenty/stado/hack/pty-bridge, /tmp",
		"stado: 13 credential paths masked (~/.ssh/id_*, ~/.aws, ~/.git-credentials, …)",
	})
	// 80x18: the wrapped banner (~10 rows) cannot coexist with the input
	// box + hint unless it's capped — the deterministic version of the
	// 80x24-with-plugins clip the E2E suite hit.
	const w, h = 80, 18
	out := m.renderLanding(w, h)

	// The input placeholder must remain visible — the whole point.
	if !strings.Contains(out, "Type a message") {
		t.Errorf("input placeholder pushed off-screen by the banner at %dx%d:\n%s", w, h, out)
	}
	// Output must fit the height budget (no overflow past row h).
	if gotH := lipgloss.Height(out); gotH > h {
		t.Errorf("landing rendered %d rows at height %d — overflows the terminal:\n%s", gotH, h, out)
	}
	// The banner was tall enough to require truncation, so the marker
	// must appear (and the full text still lives in scrollback).
	if !strings.Contains(out, "more — see scrollback") {
		t.Errorf("expected a '+N more — see scrollback' truncation marker for the oversized banner; out:\n%s", out)
	}

	// Narrow + short: the truncation marker itself must not overflow the
	// terminal width (Codex review on #89 — the marker is wider than ~27
	// cols and must be wrapped like the banner text, not appended raw).
	for _, narrow := range []int{20, 24} {
		nout := m.renderLanding(narrow, h)
		for i, line := range strings.Split(nout, "\n") {
			if lw := lipgloss.Width(line); lw > narrow {
				t.Errorf("at width %d, landing line %d width %d overflows (unwrapped marker?): %q", narrow, i, lw, line)
			}
		}
	}
}

// Render-level guard: the injected banner must survive renderBlock into
// the visible output, not merely live in m.blocks. (Verify by rendering,
// not by inspecting state.)
func TestInjectStartupNotices_survivesRender(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.injectStartupNotices([]string{
		"stado: warn: running without a process-containment sandbox.",
		"stado: sandbox=no-sandbox session=deadbeef (broker-mediated)",
	})
	out, err := m.renderBlock(m.blocks[len(m.blocks)-1], 80)
	if err != nil {
		t.Fatalf("renderBlock: %v", err)
	}
	// Single tokens that word-wrap won't split, so the assertion is robust
	// to lipgloss re-wrapping at the given width.
	for _, want := range []string{"process-containment", "no-sandbox", "broker-mediated"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered startup block missing %q; out=%q", want, out)
		}
	}
}
