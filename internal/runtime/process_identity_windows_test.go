//go:build windows

package runtime

import "testing"

func TestCheckedWindowsPIDRejectsOverflow(t *testing.T) {
	if ^uint(0)>>32 == 0 {
		t.Skip("int cannot represent a value above uint32 on this architecture")
	}
	overflow := int(uint64(^uint32(0)) + 1)
	if _, err := checkedWindowsPID(overflow); err == nil {
		t.Fatalf("checkedWindowsPID accepted %d", overflow)
	}
}
