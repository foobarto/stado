package trajectory

import (
	"encoding/json"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/sessioncontext"
	"github.com/foobarto/stado/pkg/agent"
	"path/filepath"
	"testing"
)

func TestRecorderProducesSignalFromRealToolOutcomes(t *testing.T) {
	dir := t.TempDir()
	r := Recorder{StateDir: dir, SessionID: "s", Principal: "alice"}
	call := agent.ToolUseBlock{ID: "1", Name: "app", Input: json.RawMessage(`{"bad":true}`)}
	result := agent.ToolResultBlock{ToolUseID: "1", Content: "invalid args", IsError: true}
	r.ToolOutcome(1, call, result)
	call.ID = "2"
	result.ToolUseID = "2"
	r.ToolOutcome(1, call, result)
	store, err := wal.Open(filepath.Join(dir, "broker", "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signals, err := sessioncontext.New(store).Signals("s", false)
	if err != nil || len(signals) != 1 || signals[0].Type != sessioncontext.SignalRepeatedToolFailure {
		t.Fatalf("signals=%v err=%v", signals, err)
	}
}
