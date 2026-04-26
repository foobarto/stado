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
	"github.com/foobarto/stado/internal/workdirpath"
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

type learningEditOptions struct {
	Summary      string
	Lesson       string
	Trigger      string
	Rationale    string
	Scope        string
	RepoID       string
	SessionID    string
	Sensitivity  string
	Tags         string
	Evidence     string
	Commits      []string
	Files        []string
	Tests        []string
	ExpiresAt    string
	ClearExpires bool
	ClearTags    bool
}

type learningDocumentOptions struct {
	Path string
}

type learningStaleOptions struct {
	Apply bool
}

type staleLesson struct {
	Item    memory.Item
	Missing []string
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
	cmd.AddCommand(
		newLearningProposeCmd(),
		newLearningListCmd(),
		newLearningShowCmd(),
		newLearningEditCmd(),
		newLearningActionCmd("approve", "Approve a lesson candidate"),
		newLearningActionCmd("reject", "Reject a candidate or approved lesson"),
		newLearningActionCmd("delete", "Delete a lesson from the active folded view"),
		newLearningSupersedeCmd(),
		newLearningDocumentCmd(),
		newLearningStaleCmd(),
		newLearningExportCmd(),
	)
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

func newLearningEditCmd() *cobra.Command {
	opts := &learningEditOptions{}
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a folded lesson item",
		Long: "Append an edit event for a folded lesson item. This is intended\n" +
			"for reviewing candidate lessons before approval, but can also update\n" +
			"approved lessons explicitly.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editLesson(cmd, args[0], opts)
		},
	}
	addLearningEditFlags(cmd, opts)
	return cmd
}

func newLearningSupersedeCmd() *cobra.Command {
	opts := &learningEditOptions{}
	cmd := &cobra.Command{
		Use:   "supersede <id>",
		Short: "Replace an approved lesson with a new version",
		Long: "Append a supersede event for an approved lesson. The old lesson\n" +
			"stays visible as superseded in review/export surfaces while the\n" +
			"replacement becomes the approved lesson returned by retrieval.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return supersedeLesson(cmd, args[0], opts)
		},
	}
	addLearningEditFlags(cmd, opts)
	return cmd
}

func newLearningActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateLessonAction(cmd, args[0], action)
		},
	}
}

func newLearningDocumentCmd() *cobra.Command {
	opts := &learningDocumentOptions{}
	cmd := &cobra.Command{
		Use:   "document <id>",
		Short: "Write a lesson to .learnings and reject it from prompt retrieval",
		Long: "Write a lesson into a Markdown note under .learnings/ and then\n" +
			"mark the lesson rejected so it stays out of prompt retrieval. The\n" +
			"command is explicit and refuses to overwrite an existing file.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return documentLesson(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.Path, "path", "", "Relative path under .learnings/ (default: generated from the lesson id)")
	return cmd
}

func newLearningStaleCmd() *cobra.Command {
	opts := &learningStaleOptions{}
	cmd := &cobra.Command{
		Use:   "stale",
		Short: "Find approved lessons whose evidence files are missing",
		Long: "Find approved lessons that cite evidence files which no longer\n" +
			"exist in the current worktree. By default this is a dry-run; pass\n" +
			"--apply to mark those lessons candidate for review.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return staleLessons(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Apply, "apply", false, "Mark stale approved lessons candidate for review")
	return cmd
}

func newLearningExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Export folded lesson items as JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openMemoryStore()
			if err != nil {
				return err
			}
			items, err := store.List(cmd.Context())
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), memory.Export{Items: filterLessons(items)})
		},
	}
}

func addLearningEditFlags(cmd *cobra.Command, opts *learningEditOptions) {
	flags := cmd.Flags()
	flags.StringVar(&opts.Summary, "summary", "", "Replacement summary")
	flags.StringVar(&opts.Lesson, "lesson", "", "Replacement lesson guidance")
	flags.StringVar(&opts.Trigger, "trigger", "", "Replacement applicability trigger")
	flags.StringVar(&opts.Rationale, "rationale", "", "Replacement rationale")
	flags.StringVar(&opts.Scope, "scope", "", "Replacement scope: global, repo, or session")
	flags.StringVar(&opts.RepoID, "repo-id", "", "Replacement repo id for repo-scoped lessons")
	flags.StringVar(&opts.SessionID, "session-id", "", "Replacement session id for session-scoped lessons")
	flags.StringVar(&opts.Sensitivity, "sensitivity", "", "Replacement sensitivity: normal, private, or secret")
	flags.StringVar(&opts.Tags, "tags", "", "Comma-separated replacement tags")
	flags.BoolVar(&opts.ClearTags, "clear-tags", false, "Clear all tags")
	flags.StringVar(&opts.Evidence, "evidence", "", "Replacement evidence note")
	flags.StringArrayVar(&opts.Commits, "commit", nil, "Replacement evidence commit SHA; repeatable")
	flags.StringArrayVar(&opts.Files, "file", nil, "Replacement evidence file path; repeatable")
	flags.StringArrayVar(&opts.Tests, "test", nil, "Replacement evidence test or verification command; repeatable")
	flags.StringVar(&opts.ExpiresAt, "expires-at", "", "Replacement expiry as RFC3339 timestamp")
	flags.BoolVar(&opts.ClearExpires, "clear-expires", false, "Clear expiry")
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

func editLesson(cmd *cobra.Command, id string, opts *learningEditOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	item, err := requireLesson(cmd, store, id)
	if err != nil {
		return err
	}
	changed, err := applyLearningEditFlags(cmd, opts, &item, "learning edit")
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("learning edit: at least one edit flag is required")
	}
	raw, err := json.Marshal(memory.UpdateRequest{Action: "edit", ID: id, Item: &item})
	if err != nil {
		return err
	}
	if err := store.Update(cmd.Context(), raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "lesson %s edited\n", id)
	return nil
}

func supersedeLesson(cmd *cobra.Command, id string, opts *learningEditOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	item, err := requireLesson(cmd, store, id)
	if err != nil {
		return err
	}
	if item.Confidence != "approved" {
		return fmt.Errorf("learning supersede: only approved lessons can be superseded; got %q", item.Confidence)
	}
	replacement := item
	replacement.ID = ""
	replacement.CreatedAt = time.Time{}
	replacement.UpdatedAt = time.Time{}
	replacement.Source = memory.Source{}
	replacement.Supersedes = nil

	changed, err := applyLearningEditFlags(cmd, opts, &replacement, "learning supersede")
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("learning supersede: at least one replacement flag is required")
	}
	raw, err := json.Marshal(memory.UpdateRequest{Action: "supersede", ID: id, Item: &replacement})
	if err != nil {
		return err
	}
	if err := store.Update(cmd.Context(), raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "lesson %s superseded\n", id)
	return nil
}

func updateLessonAction(cmd *cobra.Command, id, action string) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	if _, err := requireLesson(cmd, store, id); err != nil {
		return err
	}
	raw, err := json.Marshal(memory.UpdateRequest{Action: action, ID: id})
	if err != nil {
		return err
	}
	if err := store.Update(cmd.Context(), raw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "lesson %s %s\n", id, action)
	return nil
}

func documentLesson(cmd *cobra.Command, id string, opts *learningDocumentOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	item, err := requireLesson(cmd, store, id)
	if err != nil {
		return err
	}
	if item.Confidence == "superseded" {
		return fmt.Errorf("learning document: superseded lesson %q is already inactive", id)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, rel, err := learningDocumentTarget(cwd, item, opts.Path)
	if err != nil {
		return err
	}
	path := filepath.Join(root, rel)
	if err := writeLearningDocument(root, rel, lessonDocumentMarkdown(item)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("learning document: %s already exists", path)
		}
		return fmt.Errorf("learning document: write %s: %w", path, err)
	}
	if item.Confidence != "rejected" {
		raw, err := json.Marshal(memory.UpdateRequest{Action: "reject", ID: id})
		if err != nil {
			return err
		}
		if err := store.Update(cmd.Context(), raw); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "lesson %s documented at %s\n", id, path)
	return nil
}

func staleLessons(cmd *cobra.Command, opts *learningStaleOptions) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	items, err := store.List(cmd.Context())
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := findCurrentWorktreeRoot(cwd)
	stale := findStaleLessons(root, items)
	if len(stale) == 0 {
		fmt.Fprintln(os.Stderr, "(no stale file-linked lessons)")
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "ID                   MISSING_FILES  SUMMARY")
	for _, entry := range stale {
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-14d %s\n",
			shortMemory(entry.Item.ID, 20),
			len(entry.Missing),
			shortMemory(textutil.StripControlChars(entry.Item.Summary), 80),
		)
		for _, missing := range entry.Missing {
			fmt.Fprintf(cmd.OutOrStdout(), "  missing: %s\n", textutil.StripControlChars(missing))
		}
	}
	if !opts.Apply {
		fmt.Fprintln(os.Stderr, "dry-run: pass --apply to mark stale lessons candidate for review")
		return nil
	}
	for _, entry := range stale {
		item := entry.Item
		item.Confidence = "candidate"
		raw, err := json.Marshal(memory.UpdateRequest{Action: "edit", ID: item.ID, Item: &item})
		if err != nil {
			return err
		}
		if err := store.Update(cmd.Context(), raw); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "marked %d lesson(s) candidate for review\n", len(stale))
	return nil
}

func requireLesson(cmd *cobra.Command, store *memory.Store, id string) (memory.Item, error) {
	item, ok, err := store.Show(cmd.Context(), id)
	if err != nil {
		return memory.Item{}, err
	}
	if !ok || !memory.IsLesson(item) {
		return memory.Item{}, fmt.Errorf("lesson %q not found", id)
	}
	return item, nil
}

func findStaleLessons(root string, items []memory.Item) []staleLesson {
	stale := make([]staleLesson, 0)
	for _, item := range items {
		if !memory.IsLesson(item) || item.Confidence != "approved" {
			continue
		}
		missing := missingEvidenceFiles(root, item.Evidence.Files)
		if len(missing) > 0 {
			stale = append(stale, staleLesson{Item: item, Missing: missing})
		}
	}
	return stale
}

func missingEvidenceFiles(root string, files []string) []string {
	missing := make([]string, 0)
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		path := file
		if !filepath.IsAbs(path) {
			cleaned := filepath.Clean(path)
			if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
				continue
			}
			path = filepath.Join(root, cleaned)
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, file)
		}
	}
	return missing
}

func learningDocumentTarget(cwd string, item memory.Item, rawPath string) (string, string, error) {
	root := findCurrentWorktreeRoot(cwd)
	rel := strings.TrimSpace(rawPath)
	if rel == "" {
		rel = filepath.Join(".learnings", lessonDocumentFilename(item))
	} else {
		rel = filepath.Clean(rel)
		if filepath.IsAbs(rel) {
			return "", "", errors.New("learning document: --path must be relative")
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", "", errors.New("learning document: --path must stay under .learnings")
		}
		if rel == ".learnings" {
			return "", "", errors.New("learning document: --path must name a file under .learnings")
		}
		if rel != ".learnings" && !strings.HasPrefix(rel, ".learnings"+string(os.PathSeparator)) {
			rel = filepath.Join(".learnings", rel)
		}
	}
	return root, rel, nil
}

func writeLearningDocument(repoRoot, rel string, data []byte) error {
	rel = filepath.Clean(rel)
	if rel == ".learnings" || !strings.HasPrefix(rel, ".learnings"+string(os.PathSeparator)) {
		return errors.New("document path must stay under .learnings")
	}
	root, err := workdirpath.OpenRootNoSymlink(repoRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Lstat(".learnings"); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New(".learnings must not be a symlink")
		}
		if !info.IsDir() {
			return errors.New(".learnings is not a directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(".learnings", 0o750); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	} else {
		return err
	}
	learningRoot, err := root.OpenRoot(".learnings")
	if err != nil {
		return err
	}
	defer func() { _ = learningRoot.Close() }()
	learningRel, err := filepath.Rel(".learnings", rel)
	if err != nil {
		return err
	}
	if learningRel == "." || filepath.IsAbs(learningRel) ||
		learningRel == ".." || strings.HasPrefix(learningRel, ".."+string(os.PathSeparator)) {
		return errors.New("document path must stay under .learnings")
	}
	if dir := filepath.Dir(learningRel); dir != "." {
		if err := rejectLearningSymlinkDirs(learningRoot, dir); err != nil {
			return err
		}
		if err := workdirpath.MkdirAllRootNoSymlink(learningRoot, dir, 0o750); err != nil {
			return err
		}
	}
	f, err := learningRoot.OpenFile(learningRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func rejectLearningSymlinkDirs(root *os.Root, dir string) error {
	dir = filepath.Clean(dir)
	if dir == "." {
		return nil
	}
	parts := strings.Split(dir, string(os.PathSeparator))
	current := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink directory %q is not allowed", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", current)
		}
	}
	return nil
}

func findCurrentWorktreeRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	original := dir
	for {
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

func lessonDocumentFilename(item memory.Item) string {
	slug := strings.ToLower(textutil.StripControlChars(item.Summary))
	var b strings.Builder
	lastDash := false
	for _, r := range slug {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "lesson"
	}
	return name + "-" + item.ID + ".md"
}

func lessonDocumentMarkdown(item memory.Item) []byte {
	lesson := item.Lesson
	if strings.TrimSpace(lesson) == "" {
		lesson = item.Body
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLineMarkdown(item.Summary))
	fmt.Fprintf(&b, "## Trigger\n%s\n\n", oneLineMarkdown(item.Trigger))
	fmt.Fprintf(&b, "## Lesson\n%s\n\n", strings.TrimSpace(textutil.StripControlChars(lesson)))
	if rationale := strings.TrimSpace(textutil.StripControlChars(item.Rationale)); rationale != "" {
		fmt.Fprintf(&b, "## Rationale\n%s\n\n", rationale)
	}
	b.WriteString("## Evidence\n")
	writeEvidenceMarkdown(&b, item.Evidence)
	if len(item.Tags) > 0 {
		b.WriteString("\n## Tags\n")
		for _, tag := range item.Tags {
			fmt.Fprintf(&b, "- %s\n", oneLineMarkdown(tag))
		}
	}
	return []byte(b.String())
}

func writeEvidenceMarkdown(b *strings.Builder, evidence memory.Evidence) {
	wrote := false
	if evidence.SessionID != "" {
		fmt.Fprintf(b, "- Session: `%s`\n", oneLineMarkdown(evidence.SessionID))
		wrote = true
	}
	if len(evidence.Turns) > 0 {
		parts := make([]string, 0, len(evidence.Turns))
		for _, turn := range evidence.Turns {
			parts = append(parts, fmt.Sprint(turn))
		}
		fmt.Fprintf(b, "- Turns: %s\n", strings.Join(parts, ", "))
		wrote = true
	}
	writeEvidenceList(b, "Commits", evidence.Commits, &wrote)
	writeEvidenceList(b, "Tests", evidence.Tests, &wrote)
	writeEvidenceList(b, "Files", evidence.Files, &wrote)
	if notes := strings.TrimSpace(textutil.StripControlChars(evidence.Notes)); notes != "" {
		fmt.Fprintf(b, "- Notes: %s\n", notes)
		wrote = true
	}
	if !wrote {
		b.WriteString("- (none recorded)\n")
	}
}

func writeEvidenceList(b *strings.Builder, label string, values []string, wrote *bool) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "- %s:\n", label)
	for _, value := range values {
		fmt.Fprintf(b, "  - %s\n", oneLineMarkdown(value))
	}
	*wrote = true
}

func oneLineMarkdown(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(textutil.StripControlChars(s)), " "))
}

func applyLearningEditFlags(cmd *cobra.Command, opts *learningEditOptions, item *memory.Item, context string) (bool, error) {
	flags := cmd.Flags()
	changed := false
	scopeChanged := false
	if flags.Changed("summary") {
		item.Summary = opts.Summary
		changed = true
	}
	if flags.Changed("lesson") {
		item.Lesson = opts.Lesson
		item.Body = opts.Lesson
		changed = true
	}
	if flags.Changed("trigger") {
		item.Trigger = opts.Trigger
		changed = true
	}
	if flags.Changed("rationale") {
		item.Rationale = opts.Rationale
		changed = true
	}
	if flags.Changed("scope") {
		item.Scope = opts.Scope
		changed = true
		scopeChanged = true
	}
	if flags.Changed("repo-id") {
		item.RepoID = opts.RepoID
		changed = true
		scopeChanged = true
	}
	if flags.Changed("session-id") {
		item.SessionID = opts.SessionID
		changed = true
		scopeChanged = true
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
	if flags.Changed("evidence") {
		item.Evidence.Notes = opts.Evidence
		changed = true
	}
	if flags.Changed("commit") {
		item.Evidence.Commits = cleanStringList(opts.Commits)
		changed = true
	}
	if flags.Changed("file") {
		item.Evidence.Files = cleanStringList(opts.Files)
		changed = true
	}
	if flags.Changed("test") {
		item.Evidence.Tests = cleanStringList(opts.Tests)
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
	if scopeChanged {
		if err := normalizeLearningScope(item, context); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func normalizeLearningScope(item *memory.Item, context string) error {
	item.Scope = strings.TrimSpace(strings.ToLower(item.Scope))
	switch item.Scope {
	case "global":
		item.RepoID = ""
		item.SessionID = ""
	case "repo":
		item.SessionID = ""
		item.RepoID = strings.TrimSpace(item.RepoID)
		if item.RepoID == "" {
			repoID, err := currentRepoID()
			if err != nil {
				return fmt.Errorf("%s: repo id: %w", context, err)
			}
			item.RepoID = repoID
		}
	case "session":
		item.RepoID = ""
		item.SessionID = strings.TrimSpace(item.SessionID)
		if item.SessionID == "" {
			return fmt.Errorf("%s: --session-id is required for session scope", context)
		}
	default:
		return fmt.Errorf("%s: invalid scope %q", context, item.Scope)
	}
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
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(filepath.Join(".stado", "user-repo"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func init() {
	rootCmd.AddCommand(learningCmd)
}
