package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/schedule"
)

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Manage persistent scheduled stado runs (EP-0036)",
	Long: `Persistent scheduled agent runs. Entries are stored in
<state-dir>/schedules.json and executed via OS cron or on-demand.

Subcommands:
  create         Add a new schedule
  list           List all schedules
  rm <id>        Remove a schedule
  run-now <id>   Execute a schedule immediately
  install-cron   Write OS crontab entries for all schedules
  uninstall-cron Remove all stado crontab entries`,
}

// ---------- create ----------

var (
	schedCreateCron      string
	schedCreatePrompt    string
	schedCreateName      string
	schedCreateSessionID string
)

var scheduleCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Add a new scheduled run",
	Example: `  stado schedule create --cron "0 9 * * *" --prompt "daily standup"
  stado schedule create --cron "*/5 * * * *" --prompt "check deploy" --name ci-watch`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		store := &schedule.Store{Path: schedule.StorePath(cfg.StateDir())}
		e, err := store.Create(schedCreateCron, schedCreatePrompt, schedCreateName, schedCreateSessionID)
		if err != nil {
			return err
		}
		fmt.Printf("created  %s\n", e.ID)
		if schedCreateName != "" {
			fmt.Printf("name     %s\n", e.Name)
		}
		fmt.Printf("cron     %s\n", e.Cron)
		fmt.Printf("prompt   %s\n", e.Prompt)
		fmt.Printf("\nRun `stado schedule install-cron` to wire into OS cron.\n")
		return nil
	},
}

// ---------- list ----------

var scheduleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all schedules",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		store := &schedule.Store{Path: schedule.StorePath(cfg.StateDir())}
		entries, err := store.List()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("no schedules — create one with `stado schedule create`")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCRON\tPROMPT\tCREATED")
		for _, e := range entries {
			name := e.Name
			if name == "" {
				name = "-"
			}
			prompt := e.Prompt
			if len(prompt) > 40 {
				prompt = prompt[:37] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				e.ID[:8], name, e.Cron, prompt,
				e.Created.UTC().Format(time.RFC3339))
		}
		_ = w.Flush()
		return nil
	},
}

// ---------- rm ----------

var scheduleRmCmd = &cobra.Command{
	Use:   "rm <id>",
	Short: "Remove a schedule by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		store := &schedule.Store{Path: schedule.StorePath(cfg.StateDir())}
		if err := store.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", args[0])
		return nil
	},
}

// ---------- run-now ----------

var scheduleRunNowCmd = &cobra.Command{
	Use:   "run-now <id>",
	Short: "Execute a schedule immediately",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		store := &schedule.Store{Path: schedule.StorePath(cfg.StateDir())}
		stadoBin, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve stado binary: %w", err)
		}
		logPath := cfg.StateDir() + "/schedule-" + args[0] + ".log"
		fmt.Printf("running schedule %s → log: %s\n", args[0], logPath)
		return store.Run(args[0], stadoBin, logPath)
	},
}

// ---------- install-cron ----------

var scheduleInstallCronCmd = &cobra.Command{
	Use:   "install-cron",
	Short: "Write OS crontab entries for all active schedules",
	Long: `Adds one crontab entry per schedule entry, tagged with a # stado:<id>
sentinel so uninstall-cron can remove only stado-managed lines.
Idempotent — re-running replaces existing entries.

Requires 'crontab' on PATH. Not supported on Windows.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		store := &schedule.Store{Path: schedule.StorePath(cfg.StateDir())}
		stadoBin, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve stado binary: %w", err)
		}
		if err := store.InstallCron(stadoBin); err != nil {
			return err
		}
		fmt.Println("crontab updated — use `crontab -l` to verify")
		return nil
	},
}

// ---------- uninstall-cron ----------

var scheduleUninstallCronCmd = &cobra.Command{
	Use:   "uninstall-cron",
	Short: "Remove all stado-managed crontab entries",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := schedule.UninstallCron(); err != nil {
			return err
		}
		fmt.Println("stado crontab entries removed")
		return nil
	},
}

func init() {
	scheduleCreateCmd.Flags().StringVar(&schedCreateCron, "cron", "", `Cron expression, 5 fields: "min hour dom month dow" (required)`)
	scheduleCreateCmd.Flags().StringVar(&schedCreatePrompt, "prompt", "", "Prompt to run (required)")
	scheduleCreateCmd.Flags().StringVar(&schedCreateName, "name", "", "Human-readable label")
	scheduleCreateCmd.Flags().StringVar(&schedCreateSessionID, "session", "", "Resume this session ID on each run")
	_ = scheduleCreateCmd.MarkFlagRequired("cron")
	_ = scheduleCreateCmd.MarkFlagRequired("prompt")

	scheduleCmd.AddCommand(
		scheduleCreateCmd,
		scheduleListCmd,
		scheduleRmCmd,
		scheduleRunNowCmd,
		scheduleInstallCronCmd,
		scheduleUninstallCronCmd,
	)
	rootCmd.AddCommand(scheduleCmd)
}
