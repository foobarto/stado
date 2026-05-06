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

func TestLogTailHasProgress(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want bool
	}{
		{"empty", nil, false},
		{"prefix", []string{"PROGRESS [scanner] checking 17/256"}, true},
		{"middle", []string{"15:04:05 PROGRESS scanning hosts"}, true},
		{"none", []string{"INFO startup ok", "WARN slow path"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := logTailHasProgress(tc.in); got != tc.want {
				t.Errorf("logTailHasProgress(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
