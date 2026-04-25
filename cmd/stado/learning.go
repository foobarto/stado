package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/memory"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/textutil"
)

type learningProposeOptions struct {
	Summary     string
	Lesson      string
	Trigger     string
	Rationale   string
	Scope       string
	RepoID      string
	SessionID   string
	Sensitivity string
	Tags        string
	Evidence    string
	Commits     []string
	Files       []string
	Tests       []string
	ExpiresAt   string
}

type learningListOptions struct {
	JSON bool
}

var learningCmd = newLearningCmd()

func newLearningCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "learning",
		Short: "Propose and inspect reviewable operational lessons",
		Long: "Propose and inspect EP-16 learning/self-improvement lessons.\n" +
			"Lessons are stored as candidate items in the append-only memory\n" +
			"store and only affect prompts after explicit approval.",
	}
	cmd.AddCommand(newLearningProposeCmd(), newLearningListCmd(), newLearningShowCmd())
	return cmd
}

func newLearningProposeCmd() *cobra.Command {
	opts := &learningProposeOptions{Scope: "repo"}
	cmd := &cobra.Command{
		Use:   "propose",
		Short: "Propose a lesson candidate for review",
		RunE: func(cmd *cobra.Command, args []string) error {
			return proposeLesson(cmd.Context(), opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Summary, "summary", "", "Short action-oriented lesson summary")
	flags.StringVar(&opts.Lesson, "lesson", "", "Guidance to apply next time")
	flags.StringVar(&opts.Trigger, "trigger", "", "Situation where this lesson applies")
	flags.StringVar(&opts.Rationale, "rationale", "", "Why the lesson is valid")
	flags.StringVar(&opts.Scope, "scope", "repo", "Lesson scope: global, repo, or session")
	flags.StringVar(&opts.RepoID, "repo-id", "", "Repo id for repo-scoped lessons; defaults to the current repo")
	flags.StringVar(&opts.SessionID, "session-id", "", "Session id for session-scoped lessons")
	flags.StringVar(&opts.Sensitivity, "sensitivity", "normal", "Sensitivity: normal, private, or secret")
	flags.StringVar(&opts.Tags, "tags", "", "Comma-separated lesson tags")
	flags.StringVar(&opts.Evidence, "evidence", "", "Evidence note explaining where the lesson came from")
	flags.StringArrayVar(&opts.Commits, "commit", nil, "Evidence commit SHA; repeatable")
	flags.StringArrayVar(&opts.Files, "file", nil, "Evidence file path; repeatable")
	flags.StringArrayVar(&opts.Tests, "test", nil, "Evidence test or verification command; repeatable")
	flags.StringVar(&opts.ExpiresAt, "expires-at", "", "Optional expiry as RFC3339 timestamp")
	return cmd
}

func newLearningListCmd() *cobra.Command {
	opts := &learningListOptions{}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List lesson items",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listLessons(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Emit JSON instead of a table")
	return cmd
}

func newLearningShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one lesson item as JSON",
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
			if !ok || !memory.IsLesson(item) {
				return fmt.Errorf("lesson %q not found", args[0])
			}
			return writeJSON(cmd.OutOrStdout(), item)
		},
	}
}

func proposeLesson(ctx context.Context, opts *learningProposeOptions) error {
	if strings.TrimSpace(opts.Summary) == "" {
		return errors.New("learning propose: --summary is required")
	}
	if strings.TrimSpace(opts.Lesson) == "" {
		return errors.New("learning propose: --lesson is required")
	}
	if strings.TrimSpace(opts.Trigger) == "" {
		return errors.New("learning propose: --trigger is required")
	}
	evidence := memory.Evidence{
		Notes:   opts.Evidence,
		Commits: cleanStringList(opts.Commits),
		Files:   cleanStringList(opts.Files),
		Tests:   cleanStringList(opts.Tests),
	}
	if evidenceIsEmpty(evidence) {
		return errors.New("learning propose: evidence is required; pass --evidence, --commit, --file, or --test")
	}
	item := memory.Item{
		MemoryKind:  "lesson",
		Kind:        "lesson",
		Scope:       opts.Scope,
		Summary:     opts.Summary,
		Body:        opts.Lesson,
		Lesson:      opts.Lesson,
		Trigger:     opts.Trigger,
		Rationale:   opts.Rationale,
		Evidence:    evidence,
		Confidence:  "candidate",
		Sensitivity: opts.Sensitivity,
		Tags:        parseMemoryTags(opts.Tags),
	}
	if err := applyLearningScope(&item, opts); err != nil {
		return err
	}
	if opts.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, opts.ExpiresAt)
		if err != nil {
			return fmt.Errorf("learning propose: --expires-at must be RFC3339: %w", err)
		}
		item.ExpiresAt = expiresAt
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	if err := store.Propose(ctx, raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "lesson proposed: %s\n", opts.Summary)
	return nil
}

func applyLearningScope(item *memory.Item, opts *learningProposeOptions) error {
	scope := strings.TrimSpace(strings.ToLower(opts.Scope))
	if scope == "" {
		scope = "repo"
	}
	item.Scope = scope
	switch scope {
	case "global":
		item.RepoID = ""
		item.SessionID = ""
	case "repo":
		item.RepoID = strings.TrimSpace(opts.RepoID)
		if item.RepoID == "" {
			repoID, err := currentRepoID()
			if err != nil {
				return fmt.Errorf("learning propose: repo id: %w", err)
			}
			item.RepoID = repoID
		}
	case "session":
		item.SessionID = strings.TrimSpace(opts.SessionID)
		if item.SessionID == "" {
			return errors.New("learning propose: --session-id is required for session scope")
		}
	default:
		return fmt.Errorf("learning propose: invalid scope %q", opts.Scope)
	}
	return nil
}

func listLessons(cmd *cobra.Command, opts *learningListOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	items, err := store.List(cmd.Context())
	if err != nil {
		return err
	}
	lessons := filterLessons(items)
	if opts.JSON {
		return writeJSON(cmd.OutOrStdout(), lessons)
	}
	if len(lessons) == 0 {
		fmt.Fprintln(os.Stderr, "(no lessons)")
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "ID                   SCOPE    STATUS      UPDATED           SUMMARY")
	for _, item := range lessons {
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-8s %-11s %-17s %s\n",
			shortMemory(item.ID, 20),
			item.Scope,
			item.Confidence,
			formatMemoryTime(item.UpdatedAt),
			shortMemory(textutil.StripControlChars(item.Summary), 80),
		)
	}
	return nil
}

func filterLessons(items []memory.Item) []memory.Item {
	lessons := make([]memory.Item, 0, len(items))
	for _, item := range items {
		if memory.IsLesson(item) {
			lessons = append(lessons, item)
		}
	}
	return lessons
}

func cleanStringList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func evidenceIsEmpty(e memory.Evidence) bool {
	return strings.TrimSpace(e.Notes) == "" && len(e.Commits) == 0 && len(e.Files) == 0 && len(e.Tests) == 0
}

func currentRepoID() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return stadogit.RepoID(findCurrentRepoRoot(cwd))
}

func findCurrentRepoRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	original := dir
	for {
		if pinned := readCurrentRepoPin(dir); pinned != "" {
			return pinned
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return original
		}
		dir = parent
	}
}

func readCurrentRepoPin(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".stado", "user-repo"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func init() {
	rootCmd.AddCommand(learningCmd)
}
