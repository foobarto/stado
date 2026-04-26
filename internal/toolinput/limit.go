package toolinput

import "fmt"

const MaxBytes = 1 << 20

func CheckLen(n int) error {
	if n > MaxBytes {
		return fmt.Errorf("tool input exceeds %d bytes", MaxBytes)
	}
	return nil
}

func CheckAppend(current, delta int) error {
	if delta > MaxBytes || current > MaxBytes-delta {
		return fmt.Errorf("tool input exceeds %d bytes", MaxBytes)
	}
	return nil
}
