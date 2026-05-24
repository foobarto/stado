package envscrub

import (
	"strings"
	"testing"
)

// Codex C3/M P1 regression: Scrub must keep ONLY safelisted vars and
// pass through extra append-overrides. Any non-safelisted parent env
// var must NOT survive — that's the trust-boundary the wrapped agent
// subprocess sits on.
func TestScrub_KeepsSafelistedDropsRest(t *testing.T) {
	t.Setenv("HOME", "/tmp/h")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("ANTHROPIC_API_KEY", "sk-leak-this-please")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leak-this-too")
	t.Setenv("XDG_DATA_HOME", "/tmp/d")

	got := Scrub([]string{"FOO=bar"})

	// Safelisted entries forwarded.
	mustHave := []string{"HOME=/tmp/h", "PATH=/usr/bin", "XDG_DATA_HOME=/tmp/d"}
	for _, want := range mustHave {
		if !contains(got, want) {
			t.Errorf("Scrub dropped safelisted entry %q; got %v", want, got)
		}
	}

	// Secret-bearing names MUST NOT be in the output.
	mustNotHave := []string{"ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY"}
	for _, bad := range mustNotHave {
		for _, e := range got {
			if strings.HasPrefix(e, bad+"=") {
				t.Errorf("Scrub leaked secret-bearing var %q", e)
			}
		}
	}

	// Extra appended after safelisted (override semantics).
	if !contains(got, "FOO=bar") {
		t.Errorf("Scrub didn't append extra FOO=bar; got %v", got)
	}
}

// Empty extra still produces the safelisted set.
func TestScrub_NoExtraReturnsSafelistOnly(t *testing.T) {
	t.Setenv("HOME", "/h")
	got := Scrub(nil)
	if !contains(got, "HOME=/h") {
		t.Errorf("Scrub(nil) should still include safelisted HOME; got %v", got)
	}
}

// Safelist must NOT contain any name that looks secret-bearing — this
// is a static-analysis style guard so future safelist additions get
// reviewed before they ship. Mirrors mcpwrap's existing audit test.
func TestSafelist_NoSecretBearingNames(t *testing.T) {
	bad := []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "CREDENTIAL"}
	for _, name := range Safelist {
		upper := strings.ToUpper(name)
		for _, suffix := range bad {
			if strings.Contains(upper, suffix) {
				t.Errorf("Safelist contains suspicious name %q (matched %q) — review before shipping", name, suffix)
			}
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
