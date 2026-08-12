package tui

import (
	stadogit "github.com/foobarto/stado/internal/state/git"
	"testing"
)

func TestLearnWhileBusyQueuesUntilTurnBoundary(t *testing.T) {
	m := &Model{session: &stadogit.Session{ID: "s"}}
	m.state = stateStreaming
	focus := "tool mistakes"
	if cmd := m.startLearnReview(focus); cmd != nil {
		t.Fatal("busy learn ran immediately")
	}
	if m.pendingLearnFocus == nil || *m.pendingLearnFocus != focus {
		t.Fatalf("pending=%v", m.pendingLearnFocus)
	}
	m.state = stateIdle
	m.session = nil
	if cmd := m.runPendingLearn(); cmd != nil {
		t.Fatal("no-session review should not run")
	}
	if m.pendingLearnFocus != nil {
		t.Fatal("pending learn not cleared")
	}
}
