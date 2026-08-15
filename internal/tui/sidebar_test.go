package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/lspfind"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tools"
)

// TestDiagnosticEntryText_TruncatesByRune proves a long multi-byte message
// is truncated by RUNES, not bytes — a byte slice could split a multi-byte
// UTF-8 rune and emit invalid output (finding #6).
func TestDiagnosticEntryText_TruncatesByRune(t *testing.T) {
	// 80 multi-byte runes (each "é" is 2 bytes) — well over the 60-rune cap.
	msg := strings.Repeat("é", 80)
	out := diagnosticEntryText(lspfind.DiagnosticEntry{
		RelPath: "main.go", Line: 7, Message: msg,
	})
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
	if !strings.HasPrefix(out, "main.go:7 ") {
		t.Errorf("missing locus prefix: %q", out)
	}
	if !strings.HasSuffix(out, "…") {
		t.Errorf("expected ellipsis on a truncated message: %q", out)
	}
	// A short message passes through untouched (locus + message, no ellipsis).
	short := diagnosticEntryText(lspfind.DiagnosticEntry{
		RelPath: "x.go", Line: 1, Message: "boom",
	})
	if short != "x.go:1 boom" {
		t.Errorf("short message = %q, want %q", short, "x.go:1 boom")
	}
}

func TestSidebar_SurfacesLiveStateRisksAndNextWork(t *testing.T) {
	m := describeSlashModel(t)
	_ = m.session.NextTurn()

	m.provider = fakeCappedProvider{max: 100}
	m.sidebarDebug = true
	m.model = "qwen"
	m.providerName = "ollama"
	m.mode = modeDo
	m.state = stateStreaming
	m.turnStart = time.Now().Add(-12 * time.Second)
	m.usage.InputTokens = 82
	m.usage.CostUSD = 0.17
	m.ctxSoftThreshold = 0.70
	m.ctxHardThreshold = 0.90
	m.SetBudgetTokens(100, 200)
	m.cumulativeInputTokens = 120
	m.executor = &tools.Executor{Runner: sandbox.NoneRunner{}}
	m.queuedPrompt = "retry after reading the failing test"
	m.blocks = append(m.blocks, block{
		kind:      "tool",
		toolName:  "bash",
		startedAt: time.Now().Add(-3 * time.Second),
	})
	m.systemPromptPath = filepath.Join(t.TempDir(), "AGENTS.md")
	m.skills = []skills.Skill{{Name: "refactor"}}
	m.backgroundPlugins = []*pluginRuntime.BackgroundPlugin{{
		Manifest: plugins.Manifest{Name: "auto-compact"},
	}}
	m.recordLogLine("INFO auto-compact: threshold=10000 tokens plugin=auto-compact")
	m.todos = []todo{
		{Title: "write tests", Status: "in_progress"},
		{Title: "ship it", Status: "open"},
		{Title: "cleanup", Status: "done"},
	}

	got := m.renderSidebar(40)
	for _, want := range []string{
		"Now",
		"streaming turn",
		"tool: bash",
		"queued: retry after reading the",
		"Risk",
		"ctx 82% / hard 90%",
		"budget 120 / 200 tok",
		"sandbox: none",
		"Agent",
		"agent: Do",
		"qwen via fake",
		"instructions: AGENTS.md",
		"1 skill loaded",
		"/skill",
		"plugins: auto-compact",
		"Repo",
		"repo: " + filepath.Base(m.cwd),
		"Logs",
		"INFO auto-compact: threshold=10000",
		"plugin=auto-compact",
		"Todo",
		"2 open / 1 done",
		"write tests",
		"ship it",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sidebar missing %q\nfull output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "· Do") {
		t.Fatalf("sidebar session header should not include mode anymore\nfull output:\n%s", got)
	}
}

func TestSidebar_DefaultHidesDebugNoise(t *testing.T) {
	m := describeSlashModel(t)
	m.recordLogLine("INFO auto-compact: threshold=10000 tokens plugin=auto-compact")

	got := m.renderSidebar(40)
	for _, unwanted := range []string{
		"Risk",
		"ctx unknown",
		"budget unbounded",
		"Logs",
		"INFO auto-compact",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("default sidebar should hide %q\nfull output:\n%s", unwanted, got)
		}
	}
}
