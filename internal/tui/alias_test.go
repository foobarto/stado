package tui

import (
	"strings"
	"testing"
)

// TestAliasSlash_NoVerbListsAliases: `/alias` (no verb) lists
// existing aliases — empty fixture surfaces the "no aliases" hint
// pointing at /alias create. F-alias.
func TestAliasSlash_NoVerbListsAliases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/alias")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "No aliases") {
		t.Errorf("expected empty-list hint, got %q", body)
	}
}

// TestAliasSlash_CreateRejectsBuiltinShadow: aliases shadowing
// built-in slash commands are refused at create time so an operator
// can't break /help / /tool / /plugin etc. by accident. F-alias.
func TestAliasSlash_CreateRejectsBuiltinShadow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/alias create help /tool fs.read")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "shadows a built-in") {
		t.Errorf("expected built-in shadow rejection, got %q", body)
	}
}

// TestAliasSlash_CreateRejectsBadName: invalid alias names (spaces,
// slashes, dots, etc.) surface the validator error so the operator
// understands the shape constraint. F-alias.
func TestAliasSlash_CreateRejectsBadName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/alias create my.alias /tool fs.read")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "invalid character") {
		t.Errorf("expected invalid-character error, got %q", body)
	}
}

// TestAliasSlash_CreateRejectsBadExpansion: expansions that don't
// start with / are refused — otherwise an alias could be a free-
// form string that resolves to nothing useful. F-alias.
func TestAliasSlash_CreateRejectsBadExpansion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/alias create read tool fs.read")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "must start with /") {
		t.Errorf("expected expansion shape error, got %q", body)
	}
}

// TestAliasSlash_CreateThenListShowsEntry: a successful /alias
// create persists the entry and a subsequent /alias list surfaces
// it. Asserts the round-trip end-to-end. F-alias.
func TestAliasSlash_CreateThenListShowsEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash(`/alias create read /tool fs.read {"path":"{1}"}`)
	if body := m.blocks[len(m.blocks)-1].body; !strings.Contains(body, "/read") {
		t.Fatalf("expected create-success message naming /read, got %q", body)
	}
	m.handleSlash("/alias list")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "/read") {
		t.Errorf("list missing /read entry: %q", body)
	}
	if !strings.Contains(body, "/tool fs.read") {
		t.Errorf("list missing expansion: %q", body)
	}
}

// TestAliasSlash_RemoveDeletes: `/alias rm <name>` clears the entry
// and a subsequent /alias list reflects the removal. F-alias.
func TestAliasSlash_RemoveDeletes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash(`/alias create read /tool fs.read {"path":"{1}"}`)
	m.handleSlash("/alias rm read")
	m.handleSlash("/alias list")
	body := m.blocks[len(m.blocks)-1].body
	if strings.Contains(body, "/read") {
		t.Errorf("/alias list should not include removed alias, got %q", body)
	}
}

// TestIsReservedSlashName_CoversBuiltins: spot-check the built-in
// registry covers the most-used commands so a regression that
// drops a built-in from the set immediately fails. F-alias.
func TestIsReservedSlashName_CoversBuiltins(t *testing.T) {
	for _, name := range []string{"/help", "/tool", "/t", "/plugin", "/alias", "/clear", "/exit"} {
		if !IsReservedSlashName(name) {
			t.Errorf("%s should be in the reserved set", name)
		}
	}
	if IsReservedSlashName("/notabuiltin") {
		t.Error("/notabuiltin must not be reserved")
	}
}
