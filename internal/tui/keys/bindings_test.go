package keys

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input    string
		name     string
		expected int
	}{
		{"", "empty", 0},
		{"none", "none", 0},
		{"ctrl+a", "single", 1},
		{"ctrl+a,home", "multiple", 2},
		{"ctrl+a, home", "with space", 2},
		{"return,pagedown", "aliases", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := Parse(tt.input, tt.name)
			if len(bindings) != tt.expected {
				t.Errorf("Parse(%q) returned %d bindings, expected %d", tt.input, len(bindings), tt.expected)
			}
		})
	}
}

func TestTranslateKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"return", "enter"},
		{"pageup", "pgup"},
		{"pagedown", "pgdown"},
		{"ctrl+a", "ctrl+a"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := translateKey(tt.input)
			if result != tt.expected {
				t.Errorf("translateKey(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}
