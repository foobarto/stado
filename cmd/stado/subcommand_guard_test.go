package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestEnforceSubcommandRequired is the regression guard for the systemic
// "unknown subcommand prints help and exits 0" footgun: pure group commands
// (subcommands, no action of their own) must reject a missing/unknown
// subcommand with a nonzero exit, matching the top-level behaviour. Worst
// case was `stado completion <typo> > file` writing help into a sourced file
// and reporting success.
func TestEnforceSubcommandRequired(t *testing.T) {
	newTree := func() *cobra.Command {
		root := &cobra.Command{Use: "root", RunE: func(*cobra.Command, []string) error { return nil }}
		grp := &cobra.Command{Use: "grp"} // pure group: no Run/RunE
		var leafRan bool
		leaf := &cobra.Command{Use: "leaf", RunE: func(*cobra.Command, []string) error { leafRan = true; return nil }}
		grp.AddCommand(leaf)
		root.AddCommand(grp)
		_ = leafRan
		return root
	}

	t.Run("unknown subcommand errors", func(t *testing.T) {
		root := newTree()
		enforceSubcommandRequired(root)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"grp", "frobnicate"})
		err := root.Execute()
		if err == nil {
			t.Fatal("expected error for unknown subcommand, got nil (would exit 0)")
		}
		if !strings.Contains(err.Error(), `unknown command "frobnicate"`) {
			t.Fatalf("error = %q, want it to mention the unknown command", err)
		}
	})

	t.Run("bare group prints help without error", func(t *testing.T) {
		root := newTree()
		enforceSubcommandRequired(root)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"grp"})
		if err := root.Execute(); err != nil {
			t.Fatalf("bare group should not error, got %v", err)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("bare group should print help/usage, got %q", out.String())
		}
	})

	t.Run("valid subcommand still runs", func(t *testing.T) {
		root := newTree()
		enforceSubcommandRequired(root)
		root.SetArgs([]string{"grp", "leaf"})
		if err := root.Execute(); err != nil {
			t.Fatalf("valid subcommand should run, got %v", err)
		}
	})
}
