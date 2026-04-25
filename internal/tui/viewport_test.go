package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/tui/input"
)

func TestRenderBlocks_AutoScrollsWhenAlreadyAtBottom(t *testing.T) {
	m := uatModel(t)
	m.vp.Width = 80
	m.vp.Height = 5
	for i := 0; i < 8; i++ {
		m.blocks = append(m.blocks, block{kind: "system", body: fmt.Sprintf("line %d", i)})
	}
	m.renderBlocks()
	if !m.vp.AtBottom() {
		t.Fatalf("initial render should land at bottom, offset=%d lines=%d", m.vp.YOffset, m.vp.TotalLineCount())
	}

	m.blocks = append(m.blocks, block{kind: "system", body: "new tail"})
	m.renderBlocks()
	if !m.vp.AtBottom() {
		t.Fatalf("render while at bottom should follow new tail, offset=%d lines=%d", m.vp.YOffset, m.vp.TotalLineCount())
	}
}

func TestRenderBlocks_PreservesManualScrollUp(t *testing.T) {
	m := uatModel(t)
	m.vp.Width = 80
	m.vp.Height = 5
	for i := 0; i < 8; i++ {
		m.blocks = append(m.blocks, block{kind: "system", body: fmt.Sprintf("line %d", i)})
	}
	m.renderBlocks()
	m.vp.SetYOffset(0)

	m.blocks = append(m.blocks, block{kind: "system", body: "new tail"})
	m.renderBlocks()
	if m.vp.YOffset != 0 {
		t.Fatalf("manual scroll-up should be preserved, offset=%d", m.vp.YOffset)
	}
}

func TestView_RerendersBlocksAfterViewportWidthArrives(t *testing.T) {
	m := uatModel(t)
	m.blocks = append(m.blocks, block{kind: "system", body: "background plugin auto-compact loaded (bundled default)"})
	m.vp.Width = 0
	m.vp.Height = 5
	m.renderBlocks()
	if strings.Contains(m.vp.View(), "background plugin auto-compact") {
		t.Fatal("precondition failed: narrow fallback render unexpectedly kept the line intact")
	}

	m.width = 120
	m.height = 30
	_ = m.View()
	if !strings.Contains(m.vp.View(), "background plugin auto-compact") {
		t.Fatalf("viewport width change should rerender cached startup block, got:\n%s", m.vp.View())
	}
}

func TestSystemBlockTone(t *testing.T) {
	if got := systemBlockTone("background plugin auto-compact loaded"); got != "accent" {
		t.Fatalf("info tone = %q, want accent", got)
	}
	if got := systemBlockTone("warning: provider does not expose token counter"); got != "warning" {
		t.Fatalf("warning tone = %q, want warning", got)
	}
	if got := systemBlockTone("Provider unavailable: no provider configured"); got != "error" {
		t.Fatalf("error tone = %q, want error", got)
	}
}

func TestView_InputKeepsThreeExtraVisibleRows(t *testing.T) {
	m := uatModel(t)
	m.width = 120
	m.height = 36

	_ = m.View()
	if got := m.input.Model.Height(); got != input.DefaultVisibleRows {
		t.Fatalf("empty input height = %d, want %d", got, input.DefaultVisibleRows)
	}

	m.input.SetValue("one\ntwo")
	_ = m.View()
	want := 2 + input.ExtraVisibleRows
	if got := m.input.Model.Height(); got != want {
		t.Fatalf("two-line input height = %d, want %d", got, want)
	}
}

func TestLandingView_UsesCenteredPromptWithoutSidebar(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := uatModel(t)
	m.width = 120
	m.height = 36

	got := ansi.Strip(m.View())
	for _, want := range []string{"Type a message", "ctrl+p", "commands", "0.0.0-dev"} {
		if !strings.Contains(got, want) {
			t.Fatalf("landing view missing %q\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Now\n", "Risk\n", "Agent\n", "Repo\n"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("landing view should not render sidebar section %q\n%s", unwanted, got)
		}
	}
}

func TestRenderLandingLogo_CondensesBanner(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := ansi.Strip(renderLandingLogo(120, 40))
	lines := strings.Split(got, "\n")
	if len(lines) != landingBannerMaxHeight {
		t.Fatalf("landing logo height = %d, want %d\n%s", len(lines), landingBannerMaxHeight, got)
	}
	if !strings.ContainsAny(got, "░▒▓█") {
		t.Fatalf("landing logo lost banner art:\n%s", got)
	}
}

func TestRenderLandingLogo_UsesCompactWordmarkWhenShort(t *testing.T) {
	got := ansi.Strip(renderLandingLogo(120, 3))
	if strings.TrimSpace(got) != "stado" {
		t.Fatalf("compact landing logo = %q, want stado", got)
	}
	if strings.ContainsAny(got, "░▒▓█") {
		t.Fatalf("compact landing logo should not include dense banner art:\n%s", got)
	}
}

func TestLandingInputWidth(t *testing.T) {
	if got := landingInputWidth(120); got != 64 {
		t.Fatalf("wide landing input width = %d, want 64", got)
	}
	if got := landingInputWidth(50); got <= 0 || got > 50 {
		t.Fatalf("narrow landing input width out of bounds: %d", got)
	}
}
