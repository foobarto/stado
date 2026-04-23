package sdk

import "testing"

func TestBytesRejectsFreedPointer(t *testing.T) {
	ptr := Alloc(5)
	if ptr == 0 {
		t.Fatal("Alloc returned 0")
	}
	if wrote := Write(ptr, []byte("hello")); wrote != 5 {
		t.Fatalf("Write() wrote %d bytes, want 5", wrote)
	}

	Free(ptr, 5)

	if got := Bytes(ptr, 5); got != nil {
		t.Fatalf("Bytes() after Free = %v, want nil", got)
	}
}

func TestBytesRejectsLengthBeyondAllocation(t *testing.T) {
	ptr := Alloc(4)
	if ptr == 0 {
		t.Fatal("Alloc returned 0")
	}
	defer Free(ptr, 4)

	if got := Bytes(ptr, 5); got != nil {
		t.Fatalf("Bytes() with oversize request = %v, want nil", got)
	}
}
