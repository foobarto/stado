package envscrub

import (
	"os"
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

// v0.57.0 reconciliation regression: ScrubWithInherits restores
// EP-0032's "operator's job to manage env" trust model that PR #65/M
// silently broke. Operator names specific keys per provider via
// `inherit_env = [...]`; those (and only those, plus the safelist +
// explicit Config.Env entries) get forwarded to the wrapped agent.
// Decision: .agent/decisions/2026-05-25-acpwrap-inherit-env-opt-in.md.
func TestScrubWithInherits_ExtractsNamedKeysFromParentEnv(t *testing.T) {
	t.Setenv("HOME", "/tmp/h")
	t.Setenv("GEMINI_API_KEY", "sk-real-gemini")
	t.Setenv("OPENAI_API_KEY", "sk-real-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-real-anthropic")

	// Only inherit GEMINI; others stay scrubbed.
	got := ScrubWithInherits(nil, []string{"GEMINI_API_KEY"})

	if !contains(got, "HOME=/tmp/h") {
		t.Errorf("safelist should still pass HOME; got %v", got)
	}
	if !contains(got, "GEMINI_API_KEY=sk-real-gemini") {
		t.Errorf("inherit_env should extract GEMINI_API_KEY; got %v", got)
	}
	for _, leak := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		for _, e := range got {
			if strings.HasPrefix(e, leak+"=") {
				t.Errorf("inherit_env should NOT extract un-listed key %q; got %q", leak, e)
			}
		}
	}
}

// Explicit Config.Env entry wins on duplicate keys against inherit_env
// extraction — so a CI/sandbox operator can override a parent-env API
// key with a test-fixture value without removing the inherit_env line.
func TestScrubWithInherits_ExplicitEnvWinsOverInherit(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "sk-parent-real")

	got := ScrubWithInherits(
		[]string{"GEMINI_API_KEY=sk-sandbox-stub"},
		[]string{"GEMINI_API_KEY"},
	)

	// The explicit entry from `extra` should win.
	if !contains(got, "GEMINI_API_KEY=sk-sandbox-stub") {
		t.Errorf("explicit Config.Env should win over inherit; got %v", got)
	}
	// No duplicate — the inherit-from-parent path should skip when
	// the key is already present.
	count := 0
	for _, e := range got {
		if strings.HasPrefix(e, "GEMINI_API_KEY=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one GEMINI_API_KEY entry; got %d in %v", count, got)
	}
}

// Inherit-key that isn't actually set in the parent env should be a
// no-op (not error, not insert an empty value).
func TestScrubWithInherits_MissingInheritKeyIsNoOp(t *testing.T) {
	os.Unsetenv("STADO_TEST_NEVER_SET_KEY")
	got := ScrubWithInherits(nil, []string{"STADO_TEST_NEVER_SET_KEY"})
	for _, e := range got {
		if strings.HasPrefix(e, "STADO_TEST_NEVER_SET_KEY=") {
			t.Errorf("missing inherit key should not appear; got %q", e)
		}
	}
}
