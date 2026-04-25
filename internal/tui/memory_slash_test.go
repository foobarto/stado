package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/memory"
)

func TestMemorySlashTogglesSessionRetrieval(t *testing.T) {
	m := scenarioModel(t)
	if err := os.MkdirAll(filepath.Join(m.cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	_ = m.handleSlash("/memory off")
	if !memory.SessionDisabled(m.cwd) {
		t.Fatal("/memory off did not create session disabled marker")
	}
	if got := m.blocks[len(m.blocks)-1].body; !strings.Contains(got, "disabled for this session") {
		t.Fatalf("unexpected /memory off output: %q", got)
	}

	_ = m.handleSlash("/memory")
	if got := m.blocks[len(m.blocks)-1].body; !strings.Contains(got, "disabled for this session") {
		t.Fatalf("unexpected /memory status output: %q", got)
	}

	_ = m.handleSlash("/memory on")
	if memory.SessionDisabled(m.cwd) {
		t.Fatal("/memory on did not remove session disabled marker")
	}
	if got := m.blocks[len(m.blocks)-1].body; !strings.Contains(got, "allowed for this session") {
		t.Fatalf("unexpected /memory on output: %q", got)
	}
}
