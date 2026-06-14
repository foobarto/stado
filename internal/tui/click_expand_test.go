package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// #14 — clicking a thinking block reveals its full reasoning even when
// the display mode would otherwise tail / collapse it.

func TestIsExpandableBlock_Thinking(t *testing.T) {
	if !isExpandableBlock(block{kind: "thinking"}) {
		t.Fatal("thinking blocks should be expandable")
	}
	if !isExpandableBlock(block{kind: "tool"}) {
		t.Fatal("tool blocks should remain expandable (regression)")
	}
	if isExpandableBlock(block{kind: "user"}) {
		t.Fatal("user blocks should not be expandable")
	}
}

// In preview mode a per-block override of expanded shows the full body (no
// truncation marker), while a non-overridden block is still tailed.
func TestThinkingOverrideShowsFullBodyInPreviewMode(t *testing.T) {
	m := scenarioModel(t)
	m.vp.SetWidth(100)
	m.vp.SetHeight(20)

	var body strings.Builder
	for i := 1; i <= 12; i++ {
		body.WriteString(fmt.Sprintf("line %02d\n", i))
	}
	m.blocks = []block{{kind: "thinking", body: body.String()}}
	m.setThinkingDisplayMode(displayPreview)

	m.renderBlocks()
	preview := ansi.Strip(m.vp.View())
	if strings.Contains(preview, "line 01") || !strings.Contains(preview, "...") {
		t.Fatalf("preview mode should hide early lines + mark truncation: %q", preview)
	}

	m.blocks[0].override = overrideExpanded
	m.invalidateBlockCache(0)
	m.renderBlocks()
	full := ansi.Strip(m.vp.View())
	if !strings.Contains(full, "line 01") || !strings.Contains(full, "line 12") {
		t.Fatalf("override-expanded thinking should show full body in preview mode: %q", full)
	}
}

// A click on a rendered thinking block toggles its per-block override
// between full and one-line, in any mode.
func TestClickTogglesThinkingExpand(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	m.vp.SetWidth(100)
	m.vp.SetHeight(20)
	m.blocks = []block{{kind: "thinking", body: "reasoning one\nreasoning two"}}
	m.setThinkingDisplayMode(displayPreview)
	m.renderBlocks()

	// Locate the rendered line range for block 0 and click its start.
	startLine := -1
	for _, r := range m.blockLineRanges {
		if r.blockIdx == 0 {
			startLine = r.start
			break
		}
	}
	if startLine < 0 {
		t.Fatal("thinking block 0 was not rendered into a line range")
	}
	msgY := startLine - m.vp.YOffset() // vpTop == 0 in single-view

	if !m.handleMessagesClick(5, msgY) {
		t.Fatal("click on a thinking block should be consumed")
	}
	// Preview is not "full", so the first click forces expanded.
	if m.blocks[0].override != overrideExpanded {
		t.Fatalf("first click should force-expand the thinking block, got override=%d", m.blocks[0].override)
	}
	if m.focusedBlockIdx != 0 {
		t.Fatalf("click should focus the block, got focusedBlockIdx=%d", m.focusedBlockIdx)
	}

	if !m.handleMessagesClick(5, msgY) {
		t.Fatal("second click should also be consumed")
	}
	if m.blocks[0].override != overrideCollapsed {
		t.Fatalf("second click should force-collapse the thinking block, got override=%d", m.blocks[0].override)
	}
}
