package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose stado's environment: tools, sandbox, provider keys, paths",
	Long: "Runs a battery of non-destructive checks against the host and the\n" +
		"loaded config. Useful as a first step when something isn't working —\n" +
		"or as a pre-flight in CI to confirm stado has what it needs.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var d report
		d.check("OS/arch", runtime.GOOS+"/"+runtime.GOARCH, "ok", true)
		d.check("Go runtime", runtime.Version(), "ok", true)

		// Paths.
		d.check("Config file", cfg.ConfigPath, statusOfPath(cfg.ConfigPath), true)
		d.check("State dir", cfg.StateDir(), statusOfPath(cfg.StateDir()), true)
		d.check("Worktree dir", cfg.WorktreeDir(), statusOfPath(cfg.WorktreeDir()), true)

		// Provider selection + API key check.
		prov := cfg.Defaults.Provider
		d.check("Provider", prov, "configured", prov != "")
		if keyEnv := providerEnvName(prov); keyEnv != "" {
			if os.Getenv(keyEnv) != "" {
				d.check("Provider key  ("+keyEnv+")", "present", "ok", true)
			} else {
				d.check("Provider key  ("+keyEnv+")", "not set", "missing — stado will fail on first prompt", false)
			}
		}

		// External tools — PATH or fallback notes.
		d.checkBin("ripgrep (rg)", "rg")
		d.checkBin("ast-grep", "ast-grep")
		d.checkBin("bubblewrap (bwrap)", "bwrap")
		d.checkBin("gopls", "gopls")
		d.checkBin("git", "git")
		d.checkBin("cosign", "cosign")

		// Sandbox detection.
		d.check("Sandbox runner", sandbox.Detect().Name(), "ok", true)
		if err := sandbox.ApplyLandlock(sandbox.Policy{}); err == nil {
			d.check("Landlock", "available", "kernel ≥ 5.13", true)
		} else {
			d.check("Landlock", err.Error(), "unavailable (not fatal)", false)
		}

		d.render(cmd.OutOrStdout())
		if d.fails > 0 {
			os.Exit(2)
		}
		return nil
	},
}

type reportRow struct {
	label  string
	value  string
	detail string
	ok     bool
}

type report struct {
	rows  []reportRow
	fails int
}

func (r *report) check(label, value, detail string, ok bool) {
	r.rows = append(r.rows, reportRow{label: label, value: value, detail: detail, ok: ok})
	if !ok {
		r.fails++
	}
}

func (r *report) checkBin(label, bin string) {
	full, err := exec.LookPath(bin)
	if err != nil {
		r.check(label, "not found on PATH", "missing (install hint via --help)", false)
		return
	}
	r.check(label, full, "ok", true)
}

func (r *report) render(w fmtWriter) {
	maxLabel := 0
	for _, row := range r.rows {
		if len(row.label) > maxLabel {
			maxLabel = len(row.label)
		}
	}
	for _, row := range r.rows {
		mark := "✓"
		if !row.ok {
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %-*s  %s  (%s)\n", mark, maxLabel, row.label, row.value, row.detail)
	}
	if r.fails > 0 {
		fmt.Fprintf(w, "\n%d check(s) failed\n", r.fails)
	} else {
		fmt.Fprintln(w, "\nall checks passed")
	}
}

type fmtWriter interface {
	Write([]byte) (int, error)
}

func statusOfPath(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "does not exist yet"
	}
	if fi.IsDir() {
		return "exists (dir)"
	}
	return "exists (file)"
}

func providerEnvName(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "google", "gemini":
		return "GEMINI_API_KEY"
	}
	// Check bundled OAI-compat preset map for hosted services.
	envs := map[string]string{
		"groq":       "GROQ_API_KEY",
		"openrouter": "OPENROUTER_API_KEY",
		"deepseek":   "DEEPSEEK_API_KEY",
		"xai":        "XAI_API_KEY",
		"mistral":    "MISTRAL_API_KEY",
		"cerebras":   "CEREBRAS_API_KEY",
		"litellm":    "LITELLM_API_KEY",
	}
	if v, ok := envs[strings.ToLower(provider)]; ok {
		return v
	}
	return ""
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
