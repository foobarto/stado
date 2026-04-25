package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
)

func TestStatusSlashOpensModal(t *testing.T) {
	m := scenarioModel(t)

	_ = m.handleSlash("/status")

	if !m.showStatus {
		t.Fatal("/status should open the status modal")
	}
	out := m.View()
	for _, want := range []string{"Status", "Agent", "Runtime", "Context", "Extensions", "provider", "lsp", "activates when supported files are read"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status modal missing %q: %q", want, out)
		}
	}
}

func TestStatusModalShowsActionHints(t *testing.T) {
	m := scenarioModel(t)
	out := m.renderStatusModal(120, 40)

	for _, want := range []string{"/model", "/provider", "/tools", "/context", "/budget", "/plugin", "config.toml"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status modal missing action hint %q: %q", want, out)
		}
	}
}

func TestStatusModalShowsTraceID(t *testing.T) {
	m := scenarioModel(t)
	tid, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := trace.SpanIDFromHex("0102030405060708")
	if err != nil {
		t.Fatal(err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	m.SetRootContext(trace.ContextWithSpanContext(context.Background(), sc))

	out := m.renderStatusModal(140, 40)
	for _, want := range []string{"trace", tid.String()} {
		if !strings.Contains(out, want) {
			t.Fatalf("status modal missing trace value %q: %q", want, out)
		}
	}
}

func TestStatusModalShowsCredentialHealth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	m := scenarioModel(t)
	m.provider = nil
	m.providerName = "openai"

	out := m.renderStatusModal(140, 40)
	for _, want := range []string{"credentials", "missing OPENAI_API_KEY", "/model Ctrl+A"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status modal missing credential health %q: %q", want, out)
		}
	}
}

func TestStatusModalShowsLocalCredentialNotRequired(t *testing.T) {
	m := scenarioModel(t)
	m.provider = nil
	m.providerName = "lmstudio"

	out := m.renderStatusModal(140, 40)
	for _, want := range []string{"credentials", "not required by preset", "/providers"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status modal missing local credential hint %q: %q", want, out)
		}
	}
}

func TestStatusModalMCPRowNamesConfiguredServers(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{MCP: config.MCP{Servers: map[string]config.MCPServer{
		"zeta":  {},
		"alpha": {},
		"beta":  {},
		"gamma": {},
	}}}

	var got string
	for _, row := range m.statusExtensionRows() {
		if row.Key == "mcp" {
			got = row.Value
			break
		}
	}
	want := "4 configured: alpha, beta, gamma +1"
	if got != want {
		t.Fatalf("mcp status = %q, want %q", got, want)
	}
}

func TestStatusKeybindOpensAndEscClosesModal(t *testing.T) {
	m := scenarioModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !m.showStatus {
		t.Fatal("ctrl+x s should open the status modal")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showStatus {
		t.Fatal("esc should close the status modal")
	}
}

func TestStatusKeybindClosesAfterFullChord(t *testing.T) {
	m := scenarioModel(t)
	m.showStatus = true

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !m.showStatus {
		t.Fatal("ctrl+x primer alone should not close the status modal")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.showStatus {
		t.Fatal("ctrl+x s should close the status modal")
	}
}

func TestStatusCommandInPalette(t *testing.T) {
	m := scenarioModel(t)
	m.slash.Open()

	for _, r := range "status" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.showStatus {
		t.Fatal("selecting /status from command palette should open status modal")
	}
}
