package tools

import (
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// TestRegistryAllSorted asserts Registry.All returns tools sorted by Name
// regardless of registration order. Stable ordering is load-bearing for
// prompt-cache stability — see DESIGN §"Prompt-cache awareness".
func TestRegistryAllSorted(t *testing.T) {
	names := []string{"zebra", "alpha", "mango", "banana", "cherry"}

	// Register in randomised order 10 times; each call must produce the
	// same sorted slice.
	rnd := rand.New(rand.NewSource(42))
	var first []string
	for iter := 0; iter < 10; iter++ {
		order := append([]string(nil), names...)
		rnd.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

		r := NewRegistry()
		for _, n := range order {
			r.Register(stubTool{name: n, class: tool.ClassNonMutating})
		}
		got := make([]string, 0, len(names))
		for _, t := range r.All() {
			got = append(got, t.Name())
		}
		if iter == 0 {
			first = got
			continue
		}
		if !equalStrings(first, got) {
			t.Fatalf("iter %d: ordering diverged\n  first: %v\n  got:   %v", iter, first, got)
		}
	}

	// And the first result is in alphabetical order.
	want := []string{"alpha", "banana", "cherry", "mango", "zebra"}
	if !equalStrings(first, want) {
		t.Fatalf("not alphabetical\n  want: %v\n  got:  %v", want, first)
	}
}

// TestToolDefsStableAcrossRuns asserts that the []agent.ToolDef-style slice
// produced by iterating Registry.All() serialises to identical bytes
// regardless of registration order. This is the invariant PLAN §11.1.6
// ("tool-ordering test") protects.
func TestToolDefsStableAcrossRuns(t *testing.T) {
	names := []string{"grep", "read", "bash", "write", "glob"}

	rnd := rand.New(rand.NewSource(1))
	var canonical []byte
	for iter := 0; iter < 8; iter++ {
		order := append([]string(nil), names...)
		rnd.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

		r := NewRegistry()
		for _, n := range order {
			r.Register(stubTool{name: n, class: tool.ClassNonMutating})
		}

		type toolDef struct {
			Name   string         `json:"name"`
			Desc   string         `json:"description"`
			Schema map[string]any `json:"schema"`
		}
		var defs []toolDef
		for _, tl := range r.All() {
			defs = append(defs, toolDef{
				Name:   tl.Name(),
				Desc:   tl.Description(),
				Schema: tl.Schema(),
			})
		}
		buf, err := json.Marshal(defs)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if iter == 0 {
			canonical = buf
			continue
		}
		if string(canonical) != string(buf) {
			t.Fatalf("iter %d: serialised bytes diverged\n  first: %s\n  got:   %s",
				iter, canonical, buf)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
