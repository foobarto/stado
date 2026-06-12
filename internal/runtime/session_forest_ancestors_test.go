package runtime

import (
	"reflect"
	"testing"
)

func TestAncestorChainWalksParentLinks(t *testing.T) {
	// root <- a <- b  (b forked from a, a forked from root)
	forest := &Forest{Sessions: map[string]*SessionNode{
		"root": {ID: "root"},
		"a":    {ID: "a", ParentID: "root"},
		"b":    {ID: "b", ParentID: "a"},
	}}

	if got := ancestorChain(forest, "b"); !reflect.DeepEqual(got, []string{"a", "root"}) {
		t.Fatalf("ancestorChain(b) = %v, want [a root]", got)
	}
	if got := ancestorChain(forest, "a"); !reflect.DeepEqual(got, []string{"root"}) {
		t.Fatalf("ancestorChain(a) = %v, want [root]", got)
	}
	if got := ancestorChain(forest, "root"); len(got) != 0 {
		t.Fatalf("ancestorChain(root) = %v, want empty", got)
	}
	if got := ancestorChain(forest, "missing"); len(got) != 0 {
		t.Fatalf("ancestorChain(missing) = %v, want empty", got)
	}
}

func TestAncestorChainCycleGuard(t *testing.T) {
	// Corrupt loop x <-> y must not spin forever.
	forest := &Forest{Sessions: map[string]*SessionNode{
		"x": {ID: "x", ParentID: "y"},
		"y": {ID: "y", ParentID: "x"},
	}}
	got := ancestorChain(forest, "x")
	// Walks y, then sees x already visited and stops.
	if !reflect.DeepEqual(got, []string{"y"}) {
		t.Fatalf("ancestorChain(x) with cycle = %v, want [y]", got)
	}
}

func TestSessionAncestorsNilSidecar(t *testing.T) {
	got, err := SessionAncestors(nil, "", "sess")
	if err != nil {
		t.Fatalf("SessionAncestors(nil) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SessionAncestors(nil) = %v, want empty", got)
	}
}
