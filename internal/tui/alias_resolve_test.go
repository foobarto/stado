package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestSubstituteAliasPositionals_BasicSubstitution: a single {1}
// placeholder is replaced with the first positional arg. F-alias.
func TestSubstituteAliasPositionals_BasicSubstitution(t *testing.T) {
	got, err := substituteAliasPositionals(`/tool fs.read {"path":"{1}"}`, []string{"foo.txt"})
	if err != nil {
		t.Fatalf("substituteAliasPositionals: %v", err)
	}
	want := `/tool fs.read {"path":"foo.txt"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSubstituteAliasPositionals_MultipleReferences: {1}, {2}, …
// substitute in order; the same {N} can appear multiple times. F-alias.
func TestSubstituteAliasPositionals_MultipleReferences(t *testing.T) {
	got, err := substituteAliasPositionals(`/tool grep {"pattern":"{1}","path":"{2}","flag":"{1}"}`,
		[]string{"foo", "bar"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := `/tool grep {"pattern":"foo","path":"bar","flag":"foo"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSubstituteAliasPositionals_RejectsMissingArgs: a template
// referencing {1} with no args supplied produces an error pointing
// at the missing positional. F-alias.
func TestSubstituteAliasPositionals_RejectsMissingArgs(t *testing.T) {
	_, err := substituteAliasPositionals(`/tool fs.read {"path":"{1}"}`, nil)
	if err == nil {
		t.Fatal("expected error when {1} unfilled")
	}
	if !strings.Contains(err.Error(), "{1}") {
		t.Errorf("err should mention the missing positional, got %q", err)
	}
}

// TestSubstituteAliasPositionals_NoPlaceholdersIsNoop: template
// without {N} references returns unchanged regardless of supplied
// args. F-alias.
func TestSubstituteAliasPositionals_NoPlaceholdersIsNoop(t *testing.T) {
	got, err := substituteAliasPositionals(`/tool fs.list {}`, []string{"ignored"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != `/tool fs.list {}` {
		t.Errorf("expected no-op, got %q", got)
	}
}

// TestTryExpandAlias_NoMatchPassesThrough: when the first token
// isn't an alias, the function returns matched=false. F-alias.
func TestTryExpandAlias_NoMatchPassesThrough(t *testing.T) {
	cfg := &config.Config{Aliases: config.Aliases{"read": `/tool fs.read`}}
	_, matched, err := tryExpandAlias([]string{"/help"}, cfg)
	if matched {
		t.Error("/help should not match an alias")
	}
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
}

// TestTryExpandAlias_BuiltinShadowDefenseFires: even if an alias
// somehow ends up named after a built-in (e.g. operator hand-edited
// config.toml), the expansion is skipped so the built-in still
// wins. F-alias.
func TestTryExpandAlias_BuiltinShadowDefenseFires(t *testing.T) {
	cfg := &config.Config{Aliases: config.Aliases{"help": `/tool grep`}}
	_, matched, _ := tryExpandAlias([]string{"/help"}, cfg)
	if matched {
		t.Error("alias shadowing a built-in must NOT expand")
	}
}

// TestTryExpandAlias_SubstitutesAndReturns: a matched alias with
// {1} replaced by the first positional arg returns ok=true and the
// fully substituted string. F-alias.
func TestTryExpandAlias_SubstitutesAndReturns(t *testing.T) {
	cfg := &config.Config{Aliases: config.Aliases{"read": `/tool fs.read {"path":"{1}"}`}}
	got, matched, err := tryExpandAlias([]string{"/read", "foo.txt"}, cfg)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !matched {
		t.Fatal("expected match")
	}
	want := `/tool fs.read {"path":"foo.txt"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAliasSlash_EndToEndExpansion: /alias create defines the
// alias; calling the alias dispatches through to the underlying
// /tool path. The not-found error from the inner /tool fs.read
// confirms expansion fired (the test fixture has no real fs.read
// tool; we assert on the not-found surface to check the alias
// expanded correctly into a /tool dispatch).
func TestAliasSlash_EndToEndExpansion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash(`/alias create scan /tool fakescan.target {"target":"{1}"}`)

	// Now invoke the alias.
	m.handleSlash(`/scan 10.10.10.5`)
	body := m.blocks[len(m.blocks)-1].body
	// The expansion should have hit the /tool dispatcher and
	// surfaced a not-found error mentioning the inner tool name —
	// proving the alias resolved + substituted before dispatch.
	if !strings.Contains(body, `fakescan.target`) {
		t.Errorf("alias did not expand into /tool path; got %q", body)
	}
}

// TestAliasSlash_MissingPositionalSurfacesError: invoking an alias
// without enough positional args surfaces the substituteAliasPositionals
// error; downstream dispatch is skipped so the operator sees the
// alias-side message rather than a confused "tool not found". F-alias.
func TestAliasSlash_MissingPositionalSurfacesError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash(`/alias create scan /tool fakescan.target {"target":"{1}"}`)

	m.handleSlash("/scan")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "{1}") {
		t.Errorf("expected positional-missing message, got %q", body)
	}
}
