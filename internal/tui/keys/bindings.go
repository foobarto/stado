package keys

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// PrefixBinding is a multi-chord sequence (e.g. ctrl+x ctrl+b).
// The first chord primes the prefix state; the second chord resolves
// the action. Used for Emacs-style C-x keymaps.
type PrefixBinding struct {
	// Chords are the sequence of key chords that must be pressed in
	// order. For a C-x C-b binding this is ["ctrl+x", "ctrl+b"].
	Chords []string

	// HelpKey is the display string shown in help overlays.
	HelpKey string

	// Description is the human-readable action name.
	Description string
}

// IsPrefix returns true if the given binding string contains a space,
// indicating a multi-chord prefix sequence like "ctrl+x ctrl+b".
func IsPrefix(s string) bool {
	return strings.Contains(s, " ")
}

// Parse splits comma-separated binding strings into individual
// key.Bindings.  Prefix chords (containing a space, e.g.
// "ctrl+x ctrl+b") are returned as single-chord bindings using only
// their FIRST chord; use ParsePrefix to obtain full PrefixBinding
// values.  This keeps existing callers (help overlay, textarea
// key-map) working unchanged.
func Parse(input string, name string) []key.Binding {
	if input == "" || input == "none" {
		return nil
	}
	parts := strings.Split(input, ",")
	var bindings []key.Binding
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// For the flat key.Binding list, only register the first chord
		// of a prefix sequence so help text and bubbletea key maps
		// still show the trigger.
		chords := strings.Split(p, " ")
		first := translateKey(chords[0])
		bindings = append(bindings, key.NewBinding(
			key.WithKeys(first),
			key.WithHelp(first, name),
		))
	}
	return bindings
}

// ParsePrefix splits comma-separated binding strings and returns any
// prefix (multi-chord) bindings fully parsed.  Non-prefix strings are
// ignored.
func ParsePrefix(input string, name string) []PrefixBinding {
	if input == "" || input == "none" {
		return nil
	}
	parts := strings.Split(input, ",")
	var out []PrefixBinding
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || !IsPrefix(p) {
			continue
		}
		raw := strings.Split(p, " ")
		chords := make([]string, len(raw))
		for i, c := range raw {
			chords[i] = translateKey(c)
		}
		out = append(out, PrefixBinding{
			Chords:      chords,
			HelpKey:     strings.Join(chords, " "),
			Description: name,
		})
	}
	return out
}

func translateKey(k string) string {
	k = strings.ToLower(k)
	switch k {
	case "return":
		return "enter"
	case "pageup":
		return "pgup"
	case "pagedown":
		return "pgdown"
	}
	return k
}
