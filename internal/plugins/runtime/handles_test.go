package runtime

import (
	"testing"
)

// TestHandleRegistry_AllocSuccessOnEmpty: alloc on a fresh registry
// returns a non-zero id and nil error.
func TestHandleRegistry_AllocSuccessOnEmpty(t *testing.T) {
	r := newHandleRegistry()
	id, err := r.alloc("test", "value")
	if err != nil {
		t.Fatalf("alloc on empty registry should succeed; got %v", err)
	}
	if id == 0 {
		t.Errorf("alloc returned zero id")
	}
}

// TestHandleRegistry_AllocBoundExists: the retry-bound constant is
// non-zero. Locks the contract that the bound exists; the actual
// exhaustion case is impractical to test directly without mocking
// rand.Uint32 or filling 2^32 entries.
func TestHandleRegistry_AllocBoundExists(t *testing.T) {
	if maxHandleAllocAttempts <= 0 {
		t.Errorf("maxHandleAllocAttempts should be > 0; got %d", maxHandleAllocAttempts)
	}
}
