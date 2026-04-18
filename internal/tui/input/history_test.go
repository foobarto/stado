package input

import "testing"

func TestHistoryPush(t *testing.T) {
	h := NewHistory()
	h.Push("msg1")
	h.Push("msg2")
	
	if len(h.entries) != 2 {
		t.Errorf("Expected 2 entries, got %d", len(h.entries))
	}

	h.Push("msg2") // Duplicate
	if len(h.entries) != 2 {
		t.Errorf("Expected duplicate to be ignored, got %d entries", len(h.entries))
	}
}

func TestHistoryPrevNext(t *testing.T) {
	h := NewHistory()
	h.Push("msg1")
	h.Push("msg2")

	// Current buffer is "current"
	val, ok := h.Prev("current")
	if !ok || val != "msg2" {
		t.Errorf("Expected 'msg2', got %q", val)
	}
	
	val, ok = h.Prev(val)
	if !ok || val != "msg1" {
		t.Errorf("Expected 'msg1', got %q", val)
	}
	
	_, ok = h.Prev(val)
	if ok {
		t.Errorf("Expected false when reached start of history")
	}

	val, ok = h.Next()
	if !ok || val != "msg2" {
		t.Errorf("Expected 'msg2', got %q", val)
	}

	val, ok = h.Next()
	if !ok || val != "current" {
		t.Errorf("Expected temp buffer 'current', got %q", val)
	}

	_, ok = h.Next()
	if ok {
		t.Errorf("Expected false when reached end of history")
	}
}
