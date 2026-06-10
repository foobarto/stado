package keys

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPendingPrefix(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.PendingPrefix(); ok {
		t.Fatal("no prefix should be pending initially")
	}

	// Prime ctrl+x (the C-x Emacs-style prefix).
	if _, matched := r.TryPrefix(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); !matched {
		t.Fatal("ctrl+x should prime a multi-chord prefix")
	}
	pc, ok := r.PendingPrefix()
	if !ok {
		t.Fatal("PendingPrefix should be active after ctrl+x")
	}
	if pc.Prefix != "ctrl+x" {
		t.Errorf("Prefix = %q, want ctrl+x", pc.Prefix)
	}
	if len(pc.Options) == 0 {
		t.Fatal("expected next-chord options (C-x m, C-x a, …)")
	}
	for _, o := range pc.Options {
		if o.Key == "" || o.Desc == "" {
			t.Errorf("option missing key/desc: %+v", o)
		}
	}

	// Reset clears it.
	r.ResetPrefix()
	if _, ok := r.PendingPrefix(); ok {
		t.Error("PendingPrefix should be inactive after ResetPrefix")
	}
}
