package tui

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUILogger_TraceDuringUpdateDoesNotBlock(t *testing.T) {
	t.Setenv("STADO_TUI_TRACE", "1")
	traceOnce = sync.Once{}
	traceEnabled = false
	t.Cleanup(func() {
		traceOnce = sync.Once{}
		traceEnabled = false
	})

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

	if len(m.logTail) == 0 {
		t.Fatal("expected trace log to be recorded")
	}
}
