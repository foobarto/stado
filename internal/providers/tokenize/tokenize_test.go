package tokenize

import (
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestEncodeStringKnownCounts checks a handful of fixed strings against
// tiktoken's known output. Sanity check that offline loader + encoding
// selection round-trip without network.
func TestEncodeStringKnownCounts(t *testing.T) {
	cases := []struct {
		model string
		text  string
		want  int // from OpenAI tiktoken Python reference
	}{
		{"gpt-4", "hello world", 2},
		{"gpt-4", "", 0},
		{"gpt-4o", "hello world", 2},
	}
	for _, tc := range cases {
		got, err := EncodeString(tc.model, tc.text)
		if err != nil {
			t.Fatalf("%s/%q: %v", tc.model, tc.text, err)
		}
		if got != tc.want {
			t.Errorf("%s/%q: got %d want %d", tc.model, tc.text, got, tc.want)
		}
	}
}

// TestCountOAI_ScalesWithContent ensures the counter scales with payload
// size — a longer message gives a larger count.
func TestCountOAI_ScalesWithContent(t *testing.T) {
	short := agent.TurnRequest{
		Model:    "gpt-4",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
	}
	long := agent.TurnRequest{
		Model: "gpt-4",
		Messages: []agent.Message{agent.Text(agent.RoleUser,
			"this is a much longer piece of text that should definitely "+
				"tokenise into more than two or three tokens, probably around "+
				"twenty to thirty depending on how you slice it")},
	}
	a, err := CountOAI(short.Model, short)
	if err != nil {
		t.Fatalf("short: %v", err)
	}
	b, err := CountOAI(long.Model, long)
	if err != nil {
		t.Fatalf("long: %v", err)
	}
	if b <= a {
		t.Fatalf("long (%d) should exceed short (%d)", b, a)
	}
	if a < 5 || b < 30 {
		t.Errorf("counts look off: short=%d long=%d", a, b)
	}
}

// TestCountOAI_IncludesSystemAndTools confirms both System and Tools
// contribute to the total — regressions here would leak unaccounted
// tokens into the cache prefix.
func TestCountOAI_IncludesSystemAndTools(t *testing.T) {
	base := agent.TurnRequest{
		Model:    "gpt-4",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hello")},
	}
	withSystem := base
	withSystem.System = "you are a helpful assistant that answers questions concisely"

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	})
	withTools := base
	withTools.Tools = []agent.ToolDef{
		{Name: "read", Description: "Read a file", Schema: schema},
	}

	b, _ := CountOAI(base.Model, base)
	s, _ := CountOAI(withSystem.Model, withSystem)
	tl, _ := CountOAI(withTools.Model, withTools)
	if s <= b {
		t.Errorf("System-adding didn't raise count: base=%d with=%d", b, s)
	}
	if tl <= b {
		t.Errorf("Tool-adding didn't raise count: base=%d with=%d", b, tl)
	}
}

// TestCountOAI_UnknownModelFallback asserts an unrecognised model name
// doesn't error — it falls back to cl100k_base.
func TestCountOAI_UnknownModelFallback(t *testing.T) {
	req := agent.TurnRequest{
		Model:    "some-random-future-model-7b",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hello world")},
	}
	n, err := CountOAI(req.Model, req)
	if err != nil {
		t.Fatalf("unexpected error on unknown model: %v", err)
	}
	if n < 2 {
		t.Errorf("fallback count seems wrong: %d", n)
	}
}
