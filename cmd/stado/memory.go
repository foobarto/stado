package main

import (
	"encoding/json"
	"errors"
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
	Long: "List, inspect, edit, approve, reject, delete, and export the local\n" +
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

type memoryEditOptions struct {
	Summary      string
	Body         string
	Kind         string
	Scope        string
	RepoID       string
	SessionID    string
	Sensitivity  string
	Tags         string
	ExpiresAt    string
	ClearExpires bool
	ClearBody    bool
	ClearTags    bool
}

func newMemoryEditCmd() *cobra.Command {
	opts := &memoryEditOptions{}
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a folded memory item",
		Long: "Append an edit event for a folded memory item. This is intended\n" +
			"for reviewing plugin-proposed candidate memories before approval,\n" +
			"but can also update approved memories explicitly.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editMemory(cmd, args[0], opts)
		},
	}
	addMemoryEditFlags(cmd, opts)
	return cmd
}

func newMemorySupersedeCmd() *cobra.Command {
	opts := &memoryEditOptions{}
	cmd := &cobra.Command{
		Use:   "supersede <id>",
		Short: "Replace an approved memory with a new version",
		Long: "Append a supersede event for an approved memory. The old memory\n" +
			"stays visible as superseded in review/export surfaces while the\n" +
			"replacement becomes the approved item returned by memory queries.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return supersedeMemory(cmd, args[0], opts)
		},
	}
	addMemoryEditFlags(cmd, opts)
	return cmd
}

func addMemoryEditFlags(cmd *cobra.Command, opts *memoryEditOptions) {
	flags := cmd.Flags()
	flags.StringVar(&opts.Summary, "summary", "", "Replacement summary")
	flags.StringVar(&opts.Body, "body", "", "Replacement body")
	flags.BoolVar(&opts.ClearBody, "clear-body", false, "Clear the body")
	flags.StringVar(&opts.Kind, "kind", "", "Replacement kind")
	flags.StringVar(&opts.Scope, "scope", "", "Replacement scope: global, repo, or session")
	flags.StringVar(&opts.RepoID, "repo-id", "", "Replacement repo id for repo-scoped memories")
	flags.StringVar(&opts.SessionID, "session-id", "", "Replacement session id for session-scoped memories")
	flags.StringVar(&opts.Sensitivity, "sensitivity", "", "Replacement sensitivity: normal, private, or secret")
	flags.StringVar(&opts.Tags, "tags", "", "Comma-separated replacement tags")
	flags.BoolVar(&opts.ClearTags, "clear-tags", false, "Clear all tags")
	flags.StringVar(&opts.ExpiresAt, "expires-at", "", "Replacement expiry as RFC3339 timestamp")
	flags.BoolVar(&opts.ClearExpires, "clear-expires", false, "Clear expiry")
}

var memoryEditCmd = newMemoryEditCmd()
var memorySupersedeCmd = newMemorySupersedeCmd()

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

func editMemory(cmd *cobra.Command, id string, opts *memoryEditOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	item, ok, err := store.Show(cmd.Context(), id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("memory %q not found", id)
	}

	changed, err := applyMemoryEditFlags(cmd, opts, &item, "memory edit")
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("memory edit: at least one edit flag is required")
	}
	raw, err := json.Marshal(memory.UpdateRequest{Action: "edit", ID: id, Item: &item})
	if err != nil {
		return err
	}
	if err := store.Update(cmd.Context(), raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "memory %s edited\n", id)
	return nil
}

func supersedeMemory(cmd *cobra.Command, id string, opts *memoryEditOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	item, ok, err := store.Show(cmd.Context(), id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("memory %q not found", id)
	}
	if item.Confidence != "approved" {
		return fmt.Errorf("memory supersede: only approved memories can be superseded; got %q", item.Confidence)
	}

	replacement := item
	replacement.ID = ""
	replacement.CreatedAt = time.Time{}
	replacement.UpdatedAt = time.Time{}
	replacement.Source = memory.Source{}
	replacement.Supersedes = nil
	changed, err := applyMemoryEditFlags(cmd, opts, &replacement, "memory supersede")
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("memory supersede: at least one replacement flag is required")
	}

	raw, err := json.Marshal(memory.UpdateRequest{Action: "supersede", ID: id, Item: &replacement})
	if err != nil {
		return err
	}
	if err := store.Update(cmd.Context(), raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "memory %s superseded\n", id)
	return nil
}

func applyMemoryEditFlags(cmd *cobra.Command, opts *memoryEditOptions, item *memory.Item, context string) (bool, error) {
	flags := cmd.Flags()
	changed := false
	if flags.Changed("summary") {
		item.Summary = opts.Summary
		changed = true
	}
	if flags.Changed("body") {
		item.Body = opts.Body
		changed = true
	}
	if opts.ClearBody {
		item.Body = ""
		changed = true
	}
	if flags.Changed("kind") {
		item.Kind = opts.Kind
		changed = true
	}
	if flags.Changed("scope") {
		item.Scope = opts.Scope
		changed = true
	}
	if flags.Changed("repo-id") {
		item.RepoID = opts.RepoID
		changed = true
	}
	if flags.Changed("session-id") {
		item.SessionID = opts.SessionID
		changed = true
	}
	if flags.Changed("sensitivity") {
		item.Sensitivity = opts.Sensitivity
		changed = true
	}
	if flags.Changed("tags") {
		item.Tags = parseMemoryTags(opts.Tags)
		changed = true
	}
	if opts.ClearTags {
		item.Tags = nil
		changed = true
	}
	if flags.Changed("expires-at") {
		expiresAt, err := time.Parse(time.RFC3339, opts.ExpiresAt)
		if err != nil {
			return false, fmt.Errorf("%s: --expires-at must be RFC3339: %w", context, err)
		}
		item.ExpiresAt = expiresAt
		changed = true
	}
	if opts.ClearExpires {
		item.ExpiresAt = time.Time{}
		changed = true
	}
	return changed, nil
}

func parseMemoryTags(s string) []string {
	parts := strings.Split(s, ",")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
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
	memoryCmd.AddCommand(memoryListCmd, memoryShowCmd, memoryApproveCmd, memoryRejectCmd, memoryDeleteCmd, memoryEditCmd, memorySupersedeCmd, memoryExportCmd)
	rootCmd.AddCommand(memoryCmd)
}
