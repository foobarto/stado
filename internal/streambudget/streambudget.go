package streambudget

import "fmt"

const (
	MaxAssistantTextBytes = 1 << 20
	MaxThinkingTextBytes  = 1 << 20
)

func CheckAppend(label string, current, delta, maxBytes int) error {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if delta > maxBytes || current > maxBytes-delta {
		return fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return nil
}
