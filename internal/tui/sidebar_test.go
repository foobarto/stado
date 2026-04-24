package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tools"
)

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
	m.SetBudget(0.10, 2.00)
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
		"budget $0.17 / $2.00",
		"sandbox: none",
		"Agent",
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
