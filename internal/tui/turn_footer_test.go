package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/pkg/agent"
)

func TestTurnFooterIncludesCoreMetadata(t *testing.T) {
	m := scenarioModel(t)
	m.turnMode = modePlan
	m.turnModel = "qwen"
	m.turnProvider = "lmstudio"
	m.turnStart = time.Now().Add(-1500 * time.Millisecond)
	m.turnToolCalls = []agent.ToolUseBlock{{Name: "read"}, {Name: "grep"}}

	got := m.turnFooter(&agent.Usage{
		InputTokens:  1234,
		OutputTokens: 56,
		CostUSD:      0.0123,
	})
	for _, want := range []string{
		"Plan",
		"qwen via lmstudio",
		"tools 2",
		"in 1.2K out 56",
		"+$0.0123",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("footer missing %q: %q", want, got)
		}
	}
}

func TestAttachTurnFooterAnnotatesLastAssistantBlock(t *testing.T) {
	m := scenarioModel(t)
	m.turnMode = modeDo
	m.turnModel = "m"
	m.turnProvider = "p"
	m.blocks = []block{
		{kind: "assistant", body: "first"},
		{kind: "tool", toolName: "read"},
		{kind: "assistant", body: "second"},
	}

	m.attachTurnFooter(&agent.Usage{OutputTokens: 10})

	if m.blocks[0].meta != "" {
		t.Fatalf("first assistant should not be annotated: %+v", m.blocks[0])
	}
	if !strings.Contains(m.blocks[2].meta, "Do") || !strings.Contains(m.blocks[2].meta, "out 10") {
		t.Fatalf("last assistant footer not attached: %+v", m.blocks[2])
	}
}

func TestTurnDetailsIncludeCacheAndTools(t *testing.T) {
	m := scenarioModel(t)
	m.turnToolCalls = []agent.ToolUseBlock{{Name: "read"}}

	got := m.turnDetails(&agent.Usage{
		InputTokens:      1000,
		OutputTokens:     50,
		CacheReadTokens:  250,
		CacheWriteTokens: 25,
		CostUSD:          0.01,
	})
	for _, want := range []string{
		"tokens: input 1.0K, output 50",
		"cache: read 250, write 25",
		"cost: +$0.0100",
		"tools: 1 requested (read)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("turn details missing %q: %q", want, got)
		}
	}
}

func TestToolResultsAnnotateAssistantDetailsWithFailures(t *testing.T) {
	m := scenarioModel(t)
	m.blocks = []block{{
		kind:    "assistant",
		body:    "checking",
		meta:    "Do · qwen via lmstudio · tools 3",
		details: "tools: 3 requested (read, write, bash)",
	}}

	m.annotateLastAssistantToolResults([]agent.ToolResultBlock{
		{ToolUseID: "read-1", Content: "ok"},
		{ToolUseID: "write-1", Content: unavailableToolContent("write"), IsError: true},
		{ToolUseID: "bash-1", Content: "exit status 1", IsError: true},
	})

	got := m.blocks[0]
	if !strings.Contains(got.meta, "tools 3 (1 failed, 1 rejected)") {
		t.Fatalf("assistant meta missing failed/rejected counts: %q", got.meta)
	}
	if !strings.Contains(got.details, "tool results: 1 ok, 1 failed, 1 rejected") {
		t.Fatalf("assistant details missing tool result counts: %q", got.details)
	}
}

func TestAssistantBlockRendersFooter(t *testing.T) {
	m := scenarioModel(t)
	out, err := m.renderBlock(block{
		kind: "assistant",
		body: "done",
		meta: "Do · qwen via lmstudio · tools 0",
	}, 80)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "done") || !strings.Contains(out, "tools 0") {
		t.Fatalf("assistant block missing body/footer: %q", out)
	}
}

func TestAssistantBlockRendersExpandedDetails(t *testing.T) {
	m := scenarioModel(t)
	blk := block{
		kind:     "assistant",
		body:     "done",
		meta:     "Do · qwen via lmstudio · tools 0",
		details:  "cache: read 250, write 25",
		expanded: true,
	}
	out, err := m.renderBlock(blk, 80)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cache: read 250, write 25") {
		t.Fatalf("expanded assistant details missing: %q", out)
	}
}

func TestShiftTabTogglesLatestAssistantDetails(t *testing.T) {
	m := scenarioModel(t)
	m.blocks = []block{{kind: "assistant", body: "done", meta: "Do", details: "cache: read 250, write 25"}}

	m.toggleLastToolExpand()
	if !m.blocks[0].expanded {
		t.Fatal("assistant details should expand")
	}

	m.toggleLastToolExpand()
	if m.blocks[0].expanded {
		t.Fatal("assistant details should collapse")
	}
}

// TestFocusPrevNext_SkipsNonExpandable: alt+up walks back through tool
// blocks, skipping user/system blocks; alt+down moves forward; past the
// last expandable, focus clears so ToolExpand falls back to "latest."
func TestFocusPrevNext_SkipsNonExpandable(t *testing.T) {
	m := scenarioModel(t)
	m.focusedBlockIdx = -1
	m.blocks = []block{
		{kind: "user", body: "hi"},
		{kind: "tool", toolName: "fs.read", body: "..."},
		{kind: "user", body: "more"},
		{kind: "tool", toolName: "bash", body: "..."},
	}

	m.focusPrevExpandable()
	if m.focusedBlockIdx != 3 {
		t.Errorf("first prev should land on the latest tool block (idx 3), got %d", m.focusedBlockIdx)
	}
	m.focusPrevExpandable()
	if m.focusedBlockIdx != 1 {
		t.Errorf("second prev should skip user blocks and land on idx 1, got %d", m.focusedBlockIdx)
	}
	m.focusPrevExpandable()
	if m.focusedBlockIdx != 1 {
		t.Errorf("no earlier expandable — focus should stick at idx 1, got %d", m.focusedBlockIdx)
	}

	m.focusNextExpandable()
	if m.focusedBlockIdx != 3 {
		t.Errorf("next from 1 should land on idx 3, got %d", m.focusedBlockIdx)
	}
	m.focusNextExpandable()
	if m.focusedBlockIdx != -1 {
		t.Errorf("next past last expandable should clear focus, got %d", m.focusedBlockIdx)
	}
}

// TestStripTrailingSpacesPerLine: clean copy support — removes trailing
// spaces / tabs from every line, leaves embedded whitespace alone.
func TestStripTrailingSpacesPerLine(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"hello":                           "hello",
		"hello   ":                        "hello",
		"hello\tworld\t\t":                "hello\tworld",
		"line1   \nline2\t\nline3":        "line1\nline2\nline3",
		"  leading kept  ":                "  leading kept",
		"unicode kept   ":            "unicode kept", // NBSP is not ASCII space
	}
	for in, want := range cases {
		got := stripTrailingSpacesPerLine(in)
		if got != want {
			t.Errorf("strip(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBlockAtContentLine: line-range lookup correctly identifies the
// block under a given content-Y coordinate, including off-the-end and
// out-of-range cases.
func TestBlockAtContentLine(t *testing.T) {
	m := scenarioModel(t)
	m.blockLineRanges = []blockLineRange{
		{start: 0, end: 5, blockIdx: 0},
		{start: 5, end: 12, blockIdx: 1},
		{start: 12, end: 20, blockIdx: 2},
	}
	cases := map[int]int{
		0:  0,  // start of first block
		4:  0,  // last line of first block
		5:  1,  // start of second
		11: 1,  // last line of second
		12: 2,  // start of third
		19: 2,  // last line of third
		20: -1, // off the end
		-1: -1, // negative
	}
	for line, want := range cases {
		got := m.blockAtContentLine(line)
		if got != want {
			t.Errorf("blockAtContentLine(%d) = %d, want %d", line, got, want)
		}
	}
}

// TestToggleLastToolExpand_HonoursFocus: when a focused block exists,
// ToolExpand toggles it, not the latest.
func TestToggleLastToolExpand_HonoursFocus(t *testing.T) {
	m := scenarioModel(t)
	m.blocks = []block{
		{kind: "tool", toolName: "fs.read"},
		{kind: "tool", toolName: "bash"},
	}
	// Focus the older one.
	m.focusedBlockIdx = 0
	m.blocks[0].focused = true

	m.toggleLastToolExpand()
	if !m.blocks[0].expanded {
		t.Errorf("focused block (idx 0) should be expanded; got blocks: %+v", m.blocks)
	}
	if m.blocks[1].expanded {
		t.Errorf("non-focused block (idx 1) should NOT toggle when another is focused")
	}
}
