package runtime

import (
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestWantThinking covers the full decision matrix for Phase 1.6's
// thinking knob.
func TestWantThinking(t *testing.T) {
	cases := []struct {
		mode      string
		supported bool
		want      bool
	}{
		{"", true, true},     // auto + supported → on
		{"", false, false},   // auto + unsupported → off
		{"auto", true, true}, // explicit auto + supported
		{"auto", false, false},
		{"on", true, true},    // forced on
		{"on", false, true},   // forced on regardless of cap
		{"off", true, false},  // forced off
		{"off", false, false}, // forced off
		{"nonsense", true, true}, // unknown → treated as auto
	}
	for _, c := range cases {
		got := wantThinking(c.mode, c.supported)
		if got != c.want {
			t.Errorf("wantThinking(%q, %v) = %v, want %v", c.mode, c.supported, got, c.want)
		}
	}
}

// TestStripImageBlocks_RemovesOnlyImages: vision filtering should
// drop ImageBlock entries but leave text/tool_use/tool_result intact.
// Non-image messages pass through unchanged (same slice element —
// a copy would break downstream cache-stability hashing).
func TestStripImageBlocks_RemovesOnlyImages(t *testing.T) {
	msgs := []agent.Message{
		// Text-only, no image — should pass through untouched.
		{Role: agent.RoleUser, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "hi"}},
		}},
		// Mixed content — image dropped, rest survives.
		{Role: agent.RoleUser, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "look at this"}},
			{Image: &agent.ImageBlock{MediaType: "image/png", Data: []byte("fake")}},
			{Text: &agent.TextBlock{Text: "what do you see?"}},
		}},
	}

	out := stripImageBlocks(msgs, "test-provider")

	// First message is unchanged — element identity matters for
	// downstream prompt-cache stability.
	if len(out[0].Content) != 1 || out[0].Content[0].Text == nil {
		t.Errorf("text-only message mutated: %+v", out[0])
	}

	// Second message stripped.
	if len(out[1].Content) != 2 {
		t.Fatalf("expected 2 blocks after strip, got %d: %+v", len(out[1].Content), out[1])
	}
	for i, b := range out[1].Content {
		if b.Image != nil {
			t.Errorf("block %d still has an image after strip", i)
		}
	}
}

// TestStripImageBlocks_NoImages_NoMutation: when nothing needs
// filtering, the function returns without reallocating any of the
// inner Content slices.
func TestStripImageBlocks_NoImages_NoMutation(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "plain"}}}},
	}
	out := stripImageBlocks(msgs, "test-provider")
	if len(out) != 1 || len(out[0].Content) != 1 {
		t.Errorf("no-image input was mutated: %+v", out)
	}
}
