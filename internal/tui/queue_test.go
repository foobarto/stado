package tui

import "testing"

// #17 — /queue <msg> buffers a message to run when the current turn
// finishes; from idle it just runs immediately (nothing to wait for).

func countQueuedUserBlocks(m *Model) int {
	n := 0
	for _, b := range m.blocks {
		if b.kind == "user" && b.queued {
			n++
		}
	}
	return n
}

func hasSystemBlockContaining(m *Model, substr string) bool {
	for _, b := range m.blocks {
		if b.kind == "system" && contains(b.body, substr) {
			return true
		}
	}
	return false
}

func TestQueueSlash_StreamingQueues(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming

	_ = m.handleSlash("/queue do the thing")

	if m.queuedPrompt != "do the thing" {
		t.Fatalf("queuedPrompt = %q, want %q", m.queuedPrompt, "do the thing")
	}
	if countQueuedUserBlocks(m) != 1 {
		t.Fatalf("expected exactly one queued user block, got %d", countQueuedUserBlocks(m))
	}
}

func TestQueueSlash_IdlePromotesImmediately(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle

	_ = m.handleSlash("/queue hello now")

	if m.queuedPrompt != "" {
		t.Fatalf("idle /queue should promote, queuedPrompt = %q", m.queuedPrompt)
	}
	// The block is promoted to a normal (unqueued) user message.
	found := false
	for _, b := range m.blocks {
		if b.kind == "user" && b.body == "hello now" {
			found = true
			if b.queued {
				t.Fatal("promoted block should not stay marked queued")
			}
		}
	}
	if !found {
		t.Fatal("promoted user block 'hello now' not found")
	}
}

func TestQueueSlash_ReplacesExisting(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming

	_ = m.handleSlash("/queue first")
	_ = m.handleSlash("/queue second")

	if m.queuedPrompt != "second" {
		t.Fatalf("queuedPrompt = %q, want %q", m.queuedPrompt, "second")
	}
	if got := countQueuedUserBlocks(m); got != 1 {
		t.Fatalf("expected one queued block after replace, got %d", got)
	}
	if !hasSystemBlockContaining(m, "replaced") {
		t.Fatal("expected a 'replaced' notice when re-queuing")
	}
}

func TestQueueSlash_NoArgShowsUsage(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming

	_ = m.handleSlash("/queue")

	if m.queuedPrompt != "" {
		t.Fatalf("/queue with no arg should not queue, got %q", m.queuedPrompt)
	}
	if !hasSystemBlockContaining(m, "usage") {
		t.Fatal("expected a usage hint for /queue with no arg")
	}
}
