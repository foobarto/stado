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
