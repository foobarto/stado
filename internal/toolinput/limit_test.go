package toolinput

import "testing"

func TestCheckLen(t *testing.T) {
	if err := CheckLen(MaxBytes); err != nil {
		t.Fatalf("CheckLen(MaxBytes): %v", err)
	}
	if err := CheckLen(MaxBytes + 1); err == nil {
		t.Fatal("CheckLen should reject oversized input")
	}
}

func TestCheckAppend(t *testing.T) {
	if err := CheckAppend(MaxBytes-1, 1); err != nil {
		t.Fatalf("CheckAppend at limit: %v", err)
	}
	if err := CheckAppend(MaxBytes, 1); err == nil {
		t.Fatal("CheckAppend should reject growth past limit")
	}
	if err := CheckAppend(0, MaxBytes+1); err == nil {
		t.Fatal("CheckAppend should reject oversized delta")
	}
}
