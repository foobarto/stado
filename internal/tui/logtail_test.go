package tui

import (
	"context"
	"log/slog"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUILogger_TraceDuringUpdateDoesNotBlock(t *testing.T) {
	t.Setenv("STADO_TUI_TRACE", "1")

	m := uatModel(t)
	m.input.SetValue("hello")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	m.Attach(p)

	prev := slog.Default()
	slog.SetDefault(newTUILogger(m))
	defer slog.SetDefault(prev)

	done := make(chan struct{})
	go func() {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("trace logging blocked inside Model.Update")
	}

	m.logMu.Lock()
	logLines := len(m.logTail)
	m.logMu.Unlock()
	if logLines == 0 {
		t.Fatal("expected trace log to be recorded")
	}
}
