package tui

import (
	"strings"
	"testing"
)

func TestComputeTitle_IdleVsBusy(t *testing.T) {
	m := &Model{}
	m.state = stateIdle
	if got := m.computeTitle(); got != "stado" {
		t.Errorf("idle title = %q, want %q", got, "stado")
	}

	m.state = stateStreaming
	got := m.computeTitle()
	if got == "stado" {
		t.Errorf("streaming title should differ from idle, got %q", got)
	}
	if !strings.HasSuffix(got, " stado") {
		t.Errorf("streaming title should end with ' stado', got %q", got)
	}
}

func TestComputeTitle_Compacting(t *testing.T) {
	m := &Model{}
	m.state = stateIdle
	m.compacting = true
	got := m.computeTitle()
	if got == "stado" {
		t.Errorf("compacting should trigger spinner even when state is idle, got %q", got)
	}
}

func TestHandleTitleTick_AdvancesIndexWhenBusy(t *testing.T) {
	m := &Model{}
	m.state = stateStreaming
	before := m.titleSpinIdx
	_ = m.handleTitleTick()
	if m.titleSpinIdx != before+1 {
		t.Errorf("titleSpinIdx = %d, want %d (busy → advance)", m.titleSpinIdx, before+1)
	}

	m.state = stateIdle
	m.compacting = false
	beforeIdle := m.titleSpinIdx
	_ = m.handleTitleTick()
	if m.titleSpinIdx != beforeIdle {
		t.Errorf("titleSpinIdx = %d, want %d (idle → no advance)", m.titleSpinIdx, beforeIdle)
	}
}

func TestHandleTitleTick_GlyphCyclesAcrossAllFrames(t *testing.T) {
	m := &Model{}
	m.state = stateStreaming
	seen := map[rune]bool{}
	for range len(titleSpinnerGlyphs) * 2 {
		t := m.computeTitle()
		// Title is "<glyph> stado"; first rune is the glyph.
		seen[[]rune(t)[0]] = true
		m.titleSpinIdx++
	}
	if len(seen) != len(titleSpinnerGlyphs) {
		t.Errorf("expected to see all %d frames, saw %d", len(titleSpinnerGlyphs), len(seen))
	}
	for _, g := range titleSpinnerGlyphs {
		if !seen[g] {
			t.Errorf("frame %q never appeared in cycle", g)
		}
	}
}
