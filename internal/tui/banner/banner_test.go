package banner

import (
	"strings"
	"testing"
)

// TestPlainEmbedded: the plain banner is embedded and non-empty —
// catches a build-time regression where banner.txt got missed from
// the embed directive.
func TestPlainEmbedded(t *testing.T) {
	p := Plain()
	if len(p) == 0 {
		t.Fatal("plain banner is empty — //go:embed likely missing the file")
	}
	// Sanity: the banner contains a recognisable unicode block char
	// (the ░▒▓█ ramp we render from); if it doesn't, the source
	// banner.txt has been swapped for something else.
	if !strings.ContainsAny(p, "░▒▓█") {
		t.Errorf("plain banner missing unicode block chars; bad source?")
	}
}

// TestANSIEmbedded: matches the plain case. 256-colour variant.
func TestANSIEmbedded(t *testing.T) {
	a := ANSI()
	if len(a) == 0 {
		t.Fatal("ansi banner is empty")
	}
	if !strings.Contains(a, "\x1b[") {
		t.Errorf("ansi banner missing escape sequences; wrong file?")
	}
}

// TestString_NoColorHonoured: the NO_COLOR convention swaps the
// banner variant. Verified by overriding the env var for one call.
func TestString_NoColorHonoured(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := String()
	if strings.Contains(got, "\x1b[") {
		t.Error("NO_COLOR set but banner still contains escape sequences")
	}
}

// TestString_DefaultIsColor: without NO_COLOR, the coloured banner
// is returned. Keeps the "sensible default" explicit.
func TestString_DefaultIsColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	got := String()
	if !strings.Contains(got, "\x1b[") {
		t.Error("no NO_COLOR; expected coloured banner")
	}
}
