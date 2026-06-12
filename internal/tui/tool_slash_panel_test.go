package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/config"
)

// TestToolSlashResult_ClippedNotFlooded pins the P2 defect: a `/tool`
// (or `/plugin:`) invocation routed its full result through an
// un-clipped `kind:"system"` block, so a large tool output flooded
// scrollback line-for-line. It must instead land in the collapsible
// tool panel (kind:"tool"), clipped to the configured row budget with a
// "… N more line(s)" footer — the same treatment agent-loop tool calls
// already get. Reproduce by rendering the real viewport bytes.
func TestToolSlashResult_ClippedNotFlooded(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{} // default collapsed height (8)
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)

	const total = 30
	var sb strings.Builder
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "result line %02d\n", i)
	}

	onPluginRunResult(m, pluginRunResultMsg{
		plugin:  "stado-bundled-fs",
		tool:    "fs.read",
		content: sb.String(),
	})

	plain := ansi.Strip(m.vp.View())

	// The whole point: the late lines must be hidden behind the panel.
	if strings.Contains(plain, "result line 30") {
		t.Fatalf("/tool result floods scrollback — last line is visible un-clipped:\n%s", plain)
	}
	// First lines visible (panel renders a window from the top).
	if !strings.Contains(plain, "result line 01") {
		t.Fatalf("/tool result panel should show the first line:\n%s", plain)
	}
	// The collapsible-panel truncation footer must appear.
	if !strings.Contains(plain, "more line") {
		t.Fatalf("/tool result should clip with a '… N more line(s)' footer:\n%s", plain)
	}
}

// TestToolSlashResult_LandsInToolBlock asserts the result block is a
// collapsible tool block (kind:"tool"), not a system block. This is the
// structural root-cause check behind the render assertion above — a
// system block has no expand/clip machinery, so routing through "tool"
// is what makes the panel collapsible.
func TestToolSlashResult_LandsInToolBlock(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{}

	before := len(m.blocks)
	onPluginRunResult(m, pluginRunResultMsg{
		plugin:  "stado-bundled-fs",
		tool:    "fs.read",
		content: "ok",
	})
	if len(m.blocks) != before+1 {
		t.Fatalf("expected exactly one new block, got %d", len(m.blocks)-before)
	}
	blk := m.blocks[len(m.blocks)-1]
	if blk.kind != "tool" {
		t.Fatalf("/tool result block kind = %q, want \"tool\" (collapsible panel)", blk.kind)
	}
	if blk.toolName == "" {
		t.Errorf("tool block should carry a tool name for the panel header")
	}
	if blk.toolResult != "ok" {
		t.Errorf("tool block result = %q, want \"ok\"", blk.toolResult)
	}
}

// TestToolSlashResult_ErrorStillSurfaces: an error outcome must still be
// shown to the operator (we don't want to hide failures inside a
// collapsed panel they might not expand). The error text must be present
// in the rendered output regardless of which block kind carries it.
func TestToolSlashResult_ErrorStillSurfaces(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{}
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)

	onPluginRunResult(m, pluginRunResultMsg{
		plugin: "stado-bundled-fs",
		tool:   "fs.read",
		errMsg: "no such file or directory",
	})
	plain := ansi.Strip(m.vp.View())
	if !strings.Contains(plain, "no such file or directory") {
		t.Fatalf("tool error should remain visible:\n%s", plain)
	}
}
