package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

var (
	sessionAdoptApply    bool
	sessionAdoptForkTree string
	sessionAdoptJSON     bool
)

var sessionAdoptCmd = &cobra.Command{
	Use:   "adopt <parent-id> <child-id>",
	Short: "Dry-run or apply child session changes into a parent session",
	Long: "Plans adoption of changed files from <child-id> into <parent-id>.\n" +
		"Default is a dry run. Pass --apply to copy non-conflicting child\n" +
		"changes into the parent and commit subagent_adopt trace/tree metadata.\n\n" +
		"Pass --fork-tree from the worker agent.spawn result when it is non-empty;\n" +
		"an omitted fork tree means the child forked from an empty tree.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		parent, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), args[0])
		if err != nil {
			return fmt.Errorf("adopt: open parent: %w", err)
		}
		child, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), args[1])
		if err != nil {
			return fmt.Errorf("adopt: open child: %w", err)
		}
		forkTree, err := parseAdoptForkTree(sessionAdoptForkTree)
		if err != nil {
			return err
		}

		var plan runtime.SubagentAdoptionPlan
		if sessionAdoptApply {
			plan, err = runtime.AdoptSubagentChanges(parent, child, forkTree, "stado-session-adopt", cfg.Defaults.Model)
		} else {
			plan, err = runtime.PlanSubagentAdoption(parent, child, forkTree)
		}
		if sessionAdoptJSON {
			if encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(plan); encErr != nil && err == nil {
				err = encErr
			}
		} else {
			renderAdoptionPlan(cmd.OutOrStdout(), plan, sessionAdoptApply)
		}
		if errors.Is(err, runtime.ErrSubagentAdoptionConflict) {
			return fmt.Errorf("adopt: conflicts: %s", strings.Join(plan.Conflicts, ", "))
		}
		return err
	},
}

func init() {
	sessionAdoptCmd.Flags().BoolVar(&sessionAdoptApply, "apply", false,
		"Apply the adoption plan. Default is dry-run")
	sessionAdoptCmd.Flags().StringVar(&sessionAdoptForkTree, "fork-tree", "",
		"Fork tree hash from the worker result. Empty means the zero/empty tree")
	sessionAdoptCmd.Flags().BoolVar(&sessionAdoptJSON, "json", false,
		"Emit the adoption plan as JSON")
	sessionAdoptCmd.ValidArgsFunction = completeSessionIDs
	sessionCmd.AddCommand(sessionAdoptCmd)
}

func parseAdoptForkTree(raw string) (plumbing.Hash, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return plumbing.ZeroHash, nil
	}
	if len(raw) != 40 {
		return plumbing.ZeroHash, fmt.Errorf("adopt: --fork-tree must be a 40-character tree hash")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("adopt: --fork-tree must be hex: %w", err)
	}
	return plumbing.NewHash(raw), nil
}

func renderAdoptionPlan(w io.Writer, plan runtime.SubagentAdoptionPlan, apply bool) {
	status := "blocked"
	switch {
	case plan.Applied:
		status = "applied"
	case plan.CanAdopt:
		status = "ready"
	}
	fmt.Fprintf(w, "status: %s\n", status)
	if plan.ForkTree != "" {
		fmt.Fprintf(w, "fork_tree: %s\n", plan.ForkTree)
	}
	printPlanList(w, "changed_files", plan.ChangedFiles)
	printPlanList(w, "parent_changed_files", plan.ParentChangedFiles)
	printPlanList(w, "conflicts", plan.Conflicts)
	if plan.Applied {
		printPlanList(w, "adopted_files", plan.AdoptedFiles)
		if plan.AdoptedTree != "" {
			fmt.Fprintf(w, "adopted_tree: %s\n", plan.AdoptedTree)
		}
		return
	}
	if !apply {
		fmt.Fprintln(w, "dry_run: true")
		if plan.CanAdopt {
			fmt.Fprintln(w, "rerun_with: --apply")
		}
	}
}

func printPlanList(w io.Writer, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	for _, item := range items {
		fmt.Fprintf(w, "  %s\n", item)
	}
}
