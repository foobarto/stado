package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateAliasName_AcceptsValidNames: alias names are written
// to the [aliases] table key, so they must be shaped legally for
// TOML. The validator accepts ASCII letters, digits, _, and -;
// length-capped. F-alias.
func TestValidateAliasName_AcceptsValidNames(t *testing.T) {
	for _, name := range []string{"read", "scan-target", "rgrep", "x_y_z", "a", "ToolA"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAliasName(name); err != nil {
				t.Errorf("name %q rejected: %v", name, err)
			}
		})
	}
}

// TestValidateAliasName_RejectsInvalidNames: spaces, slashes,
// dots, colons, and other special chars must be rejected at
// validate time. F-alias.
func TestValidateAliasName_RejectsInvalidNames(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		{"", "empty"},
		{"foo bar", "invalid character"},
		{"foo/bar", "invalid character"},
		{"foo.bar", "invalid character"},
		{"foo:bar", "invalid character"},
		{"foo!", "invalid character"},
		{strings.Repeat("a", maxAliasNameBytes+1), "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAliasName(tc.name)
			if err == nil {
				t.Fatalf("expected rejection for %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}

// TestValidateAliasExpansion: expansion must start with / and be
// non-empty, length-capped. F-alias.
func TestValidateAliasExpansion(t *testing.T) {
	if err := ValidateAliasExpansion("/tool fs.read"); err != nil {
		t.Errorf("valid expansion rejected: %v", err)
	}
	if err := ValidateAliasExpansion(""); err == nil {
		t.Error("empty expansion should be rejected")
	}
	if err := ValidateAliasExpansion("not-a-slash-command"); err == nil {
		t.Error("non-/-prefixed expansion should be rejected")
	}
	if err := ValidateAliasExpansion("/" + strings.Repeat("x", maxAliasExpansionBytes)); err == nil {
		t.Error("oversize expansion should be rejected")
	}
}

// TestWriteAliasAdd_RoundTrips: writing an alias persists to TOML
// and Load picks it up under cfg.Aliases. Operator's design choice
// — global ~/.config/stado/config.toml only. F-alias.
func TestWriteAliasAdd_RoundTrips(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path := filepath.Join(tmp, "stado", "config.toml")
	if err := WriteAliasAdd(path, "read", `/tool fs.read {"path":"{1}"}`); err != nil {
		t.Fatalf("WriteAliasAdd: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Aliases["read"]; got != `/tool fs.read {"path":"{1}"}` {
		t.Errorf("alias missing or wrong: %q", got)
	}
}

// TestWriteAliasRemove_Idempotent: removing a non-existent alias is
// a no-op so /alias rm scripts can run on cold configs without
// erroring. F-alias.
func TestWriteAliasRemove_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path := filepath.Join(tmp, "stado", "config.toml")
	if err := WriteAliasRemove(path, "never-existed"); err != nil {
		t.Fatalf("removing absent alias should not error: %v", err)
	}
}
