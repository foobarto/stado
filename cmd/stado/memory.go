package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/textutil"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Review plugin-proposed persistent memories",
	Long: "List, inspect, approve, reject, delete, and export the local\n" +
		"append-only memory store used by plugins that declare memory:*\n" +
		"capabilities. Candidate memories are not injected into prompts;\n" +
		"only approved, scoped, non-secret memories are queryable.",
}

var memoryListJSON bool

var memoryListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List memory items",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openMemoryStore()
		if err != nil {
			return err
		}
		items, err := store.List(cmd.Context())
		if err != nil {
			return err
		}
		if memoryListJSON {
			return writeJSON(cmd.OutOrStdout(), items)
		}
		if len(items) == 0 {
			fmt.Fprintln(os.Stderr, "(no memories)")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "ID                   SCOPE    STATUS      SENS      UPDATED           SUMMARY")
		for _, item := range items {
			fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-8s %-11s %-9s %-17s %s\n",
				shortMemory(item.ID, 20),
				item.Scope,
				item.Confidence,
				item.Sensitivity,
				formatMemoryTime(item.UpdatedAt),
				shortMemory(textutil.StripControlChars(item.Summary), 80),
			)
		}
		return nil
	},
}

var memoryShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one memory item as JSON",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openMemoryStore()
		if err != nil {
			return err
		}
		item, ok, err := store.Show(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("memory %q not found", args[0])
		}
		return writeJSON(cmd.OutOrStdout(), item)
	},
}

var memoryApproveCmd = &cobra.Command{
	Use:   "approve <id>",
	Short: "Approve a candidate memory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return updateMemory(cmd, args[0], "approve")
	},
}

var memoryRejectCmd = &cobra.Command{
	Use:   "reject <id>",
	Short: "Reject a candidate or approved memory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return updateMemory(cmd, args[0], "reject")
	},
}

var memoryDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a memory from the active folded view",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return updateMemory(cmd, args[0], "delete")
	},
}

var memoryExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export folded memory items as JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openMemoryStore()
		if err != nil {
			return err
		}
		export, err := store.Export(cmd.Context())
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), export)
	},
}

func updateMemory(cmd *cobra.Command, id, action string) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(memory.UpdateRequest{Action: action, ID: id})
	if err != nil {
		return err
	}
	if err := store.Update(cmd.Context(), raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "memory %s %s\n", id, action)
	return nil
}

func openMemoryStore() (*memory.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return &memory.Store{
		Path:  filepath.Join(cfg.StateDir(), "memory", "memory.jsonl"),
		Actor: "stado-cli",
	}, nil
}

func writeJSON(w interface {
	Write([]byte) (int, error)
}, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func formatMemoryTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func shortMemory(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	if max <= 3 {
		return string(r[:max])
	}
	return strings.TrimSpace(string(r[:max-3])) + "..."
}

func init() {
	memoryListCmd.Flags().BoolVar(&memoryListJSON, "json", false, "Emit JSON instead of a table")
	memoryCmd.AddCommand(memoryListCmd, memoryShowCmd, memoryApproveCmd, memoryRejectCmd, memoryDeleteCmd, memoryExportCmd)
	rootCmd.AddCommand(memoryCmd)
}
