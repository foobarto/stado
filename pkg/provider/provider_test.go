package provider

import "testing"

func TestEventTypeConstants(t *testing.T) {
	events := []EventType{
		EventTextDelta,
		EventToolCallStart,
		EventToolCallArgsDelta,
		EventToolCallEnd,
		EventUsage,
		EventDone,
		EventError,
	}

	for i, e := range events {
		if int(e) != i {
			t.Errorf("EventType[%d] = %d, want %d", i, e, i)
		}
	}
}
