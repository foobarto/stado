package tools_test

import (
	"testing"

	"github.com/foobarto/stado/internal/tools"
)

func TestWireForm(t *testing.T) {
	cases := []struct{ alias, name, want string }{
		{"fs", "read", "fs__read"},
		{"fs", "write", "fs__write"},
		{"shell", "exec", "shell__exec"},
		{"htb-lab", "spawn", "htb_lab__spawn"},
		{"web", "fetch", "web__fetch"},
		{"tools", "search", "tools__search"},
		{"tools", "describe", "tools__describe"},
		{"tools", "categories", "tools__categories"},
		{"tools", "in_category", "tools__in_category"},
	}
	for _, c := range cases {
		got, err := tools.WireForm(c.alias, c.name)
		if err != nil {
			t.Errorf("WireForm(%q,%q) error: %v", c.alias, c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("WireForm(%q,%q) = %q, want %q", c.alias, c.name, got, c.want)
		}
	}
}

func TestWireForm_TooLong(t *testing.T) {
	// alias(33) + "__"(2) + name(33) = 68 > 64
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 33 chars
	_, err := tools.WireForm(long, long)
	if err == nil {
		t.Error("expected error for wire form > 64 chars")
	}
}

func TestWireForm_ReservedSeparator(t *testing.T) {
	if _, err := tools.WireForm("foo__bar", "baz"); err == nil {
		t.Error("expected error: alias contains __")
	}
	if _, err := tools.WireForm("foo", "bar__baz"); err == nil {
		t.Error("expected error: tool name contains __")
	}
}

func TestWireForm_DotsAndDashes(t *testing.T) {
	// dots and dashes both become underscores
	got, err := tools.WireForm("my.plugin", "do-thing")
	if err != nil {
		t.Fatal(err)
	}
	if got != "my_plugin__do_thing" {
		t.Errorf("got %q, want my_plugin__do_thing", got)
	}
}

func TestParseWireForm(t *testing.T) {
	alias, name, ok := tools.ParseWireForm("fs__read")
	if !ok || alias != "fs" || name != "read" {
		t.Errorf("ParseWireForm(fs__read) = %q,%q,%v", alias, name, ok)
	}

	alias, name, ok = tools.ParseWireForm("htb_lab__spawn")
	if !ok || alias != "htb_lab" || name != "spawn" {
		t.Errorf("ParseWireForm(htb_lab__spawn) = %q,%q,%v", alias, name, ok)
	}

	_, _, ok = tools.ParseWireForm("nounderscores")
	if ok {
		t.Error("expected ok=false for no __ separator")
	}

	_, _, ok = tools.ParseWireForm("single_underscore")
	if ok {
		t.Error("expected ok=false for single underscore")
	}
}
