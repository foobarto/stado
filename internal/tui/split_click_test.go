package tui

import "testing"

// Split-view click-to-expand regression tests.
//
// renderSplitPanes splits m.blocks across two viewports (activity on
// top, conversation on bottom). The mouse-click handler maps a screen
// row back to a block via per-pane line-range tables. Before the fix,
// renderSplitPanes never populated those tables, so:
//   - convo-pane clicks resolved against stale single-view ranges (wrong
//     block, or nothing); and
//   - tool blocks in the activity pane couldn't be expanded by click at
//     all (the handler's window only covered the bottom pane).
//
// These tests drive the model into split view and assert a click on an
// expandable block toggles THAT block.

// splitClickModel builds a model in split view with a deterministic mix
// of activity (system + tool) and conversation (user + thinking) blocks,
// renders a real frame so the panes are sized as on screen, and returns
// the model plus the indices of the expandable blocks.
func splitClickModel(t *testing.T) (m *Model, toolIdx, thinkingIdx int) {
	t.Helper()
	m = scenarioModel(t)
	m.width, m.height = 120, 40

	m.appendBlock(block{kind: "system", body: "startup activity line"})
	toolIdx = len(m.blocks)
	m.appendBlock(block{kind: "tool", toolName: "bash", toolArgs: "{}", toolResult: "ok"})
	m.appendBlock(block{kind: "user", body: "please think"})
	thinkingIdx = len(m.blocks)
	m.appendBlock(block{kind: "thinking", body: "reasoning step one\nreasoning step two"})

	m.splitView = true
	m.renderBlocks()
	// Drive a frame so the viewports get their real on-screen geometry,
	// then re-render so the line-range tables reflect that geometry.
	_ = m.viewString()
	m.renderBlocks()
	return m, toolIdx, thinkingIdx
}

// A click on an expandable block in the bottom (conversation) pane must
// toggle that exact block. Regression: split-view clicks used to resolve
// against stale single-view ranges.
func TestSplitView_ConvoPaneClickTogglesCorrectBlock(t *testing.T) {
	m, _, thinkingIdx := splitClickModel(t)
	vpTop := m.activityVP.Height() + 1 // +1 separator row

	consumed := false
	for dy := 0; dy < m.vp.Height(); dy++ {
		if m.handleMessagesClick(2, vpTop+dy) {
			consumed = true
			break
		}
	}
	if !consumed {
		t.Fatal("no click in the convo pane resolved to an expandable block")
	}
	if m.blocks[thinkingIdx].override != overrideExpanded {
		t.Errorf("convo-pane click did not expand the thinking block (split-view click mapping broken)")
	}
}

// A click on a tool block in the top (activity) pane must expand it.
// Regression: activity-pane clicks used to be ignored entirely, so a
// tool block could never be expanded by click in split view.
func TestSplitView_ActivityPaneClickExpandsTool(t *testing.T) {
	m, toolIdx, _ := splitClickModel(t)

	consumed := false
	for dy := 0; dy < m.activityVP.Height(); dy++ {
		if m.handleMessagesClick(2, dy) {
			consumed = true
			break
		}
	}
	if !consumed {
		t.Fatal("no click in the activity pane resolved to an expandable block")
	}
	if m.blocks[toolIdx].override != overrideExpanded {
		t.Errorf("activity-pane click did not expand the tool block (split-view click mapping broken)")
	}
}

// A click on the separator row between the two panes consumes nothing.
func TestSplitView_SeparatorClickIsNoop(t *testing.T) {
	m, _, _ := splitClickModel(t)
	sepRow := m.activityVP.Height() // the separator sits just below the activity pane
	if m.handleMessagesClick(2, sepRow) {
		t.Error("a click on the pane separator should not be consumed")
	}
}

// Control: the same convo-pane click pattern works in single view,
// proving the activity-pane/convo-pane defect was split-specific.
func TestSingleView_ConvoPaneClickTogglesCorrectBlock(t *testing.T) {
	m := scenarioModel(t)
	m.width, m.height = 120, 40
	m.appendBlock(block{kind: "system", body: "startup activity line"})
	m.appendBlock(block{kind: "tool", toolName: "bash", toolArgs: "{}", toolResult: "ok"})
	m.appendBlock(block{kind: "user", body: "please think"})
	thinkingIdx := len(m.blocks)
	m.appendBlock(block{kind: "thinking", body: "reasoning step one\nreasoning step two"})

	m.renderBlocks()
	_ = m.viewString()
	m.renderBlocks()

	// Click the thinking block specifically (other expandable blocks —
	// the tool — sit above it in single view, so click its own row).
	var thinkRow int
	for _, r := range m.blockLineRanges {
		if r.blockIdx == thinkingIdx {
			thinkRow = r.start
		}
	}
	if !m.handleMessagesClick(2, thinkRow-m.vp.YOffset()) || m.blocks[thinkingIdx].override != overrideExpanded {
		t.Errorf("single-view click should expand the thinking block (override=%d)",
			m.blocks[thinkingIdx].override)
	}
}
