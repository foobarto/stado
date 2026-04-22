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

func TestIsPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"ctrl+a", false},
		{"ctrl+x ctrl+b", true},
		{"ctrl+x ctrl+c, ctrl+b", true}, // contains a space
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsPrefix(tt.input)
			if got != tt.expected {
				t.Errorf("IsPrefix(%q) = %v, expected %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParsePrefix(t *testing.T) {
	tests := []struct {
		input         string
		expectedCount int
		expectedKey   string
	}{
		{"ctrl+x ctrl+b", 1, "ctrl+x ctrl+b"},
		{"ctrl+a", 0, ""},
		{"ctrl+x ctrl+b, ctrl+x ctrl+c", 2, "ctrl+x ctrl/b"}, // not checking key here for 2
		{"", 0, ""},
		{"none", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParsePrefix(tt.input, "test")
			if len(got) != tt.expectedCount {
				t.Errorf("ParsePrefix(%q).len = %d, expected %d", tt.input, len(got), tt.expectedCount)
			}
			if tt.expectedCount == 1 && len(got) > 0 {
				if got[0].HelpKey != tt.expectedKey {
					t.Errorf("ParsePrefix(%q)[0].HelpKey = %q, expected %q", tt.input, got[0].HelpKey, tt.expectedKey)
				}
			}
		})
	}
}

func TestParse_PrefixRegisterFirstChord(t *testing.T) {
	// Parse() on a prefix string should register only the FIRST chord
	// so help text and bubbletea key maps still show the primer.
	bindings := Parse("ctrl+x ctrl+b", "test")
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}
	keys := bindings[0].Keys()
	if len(keys) != 1 || keys[0] != "ctrl+x" {
		t.Errorf("expected ['ctrl+x'], got %v", keys)
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
