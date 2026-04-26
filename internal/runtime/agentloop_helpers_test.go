package runtime

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/streambudget"
	"github.com/foobarto/stado/pkg/agent"
)

func TestCollectTurnRejectsOversizedAssistantText(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{
		Kind: agent.EvTextDelta,
		Text: strings.Repeat("x", streambudget.MaxAssistantTextBytes+1),
	}
	close(ch)

	_, _, _, err := collectTurn(ch, nil)
	if err == nil {
		t.Fatal("expected oversized assistant text to fail")
	}
	if !strings.Contains(err.Error(), "assistant text exceeds") {
		t.Fatalf("error = %v", err)
	}
}
