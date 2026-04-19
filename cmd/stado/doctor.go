package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/localdetect"
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

		// Context management readiness (Phase 11).
		checkContext(&d, cfg)

		// Local inference autodetection — probe ollama / llamacpp /
		// vllm / lmstudio endpoints so the report tells the user
		// "you have lmstudio running at localhost:1234 with 3 models
		// loaded" without requiring them to set up a provider first.
		// Merges in user-configured presets at local-looking endpoints
		// so custom ports get probed too.
		checkLocalProviders(cmd.Context(), &d, cfg)

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

// checkLocalProviders probes each bundled local-runner endpoint + user
// presets that look local + adds one row per. Concurrent probes →
// total wait bounded by localdetect.DefaultTimeout (1s).
func checkLocalProviders(ctx context.Context, d *report, cfg *config.Config) {
	if ctx == nil {
		ctx = context.Background()
	}
	user := map[string]string{}
	if cfg != nil {
		for name, p := range cfg.Inference.Presets {
			user[name] = p.Endpoint
		}
	}
	results := localdetect.Detect(ctx, localdetect.MergeUserPresets(user))
	for _, r := range results {
		label := "Local " + r.Name
		switch {
		case !r.Reachable:
			d.check(label, r.Endpoint, "not running (probe: "+sanitizeErr(r.Err)+")", true)
		case len(r.Models) == 0:
			d.check(label, r.Endpoint, "running · no models loaded", true)
		default:
			preview := strings.Join(r.Models, ", ")
			if len(preview) > 80 {
				preview = preview[:79] + "…"
			}
			detail := fmt.Sprintf("running · %d model(s): %s", len(r.Models), preview)
			d.check(label, r.Endpoint, detail, true)
		}
	}
}

// sanitizeErr trims long dial-error messages to a short reason the
// doctor report can fit on one line.
func sanitizeErr(err error) string {
	if err == nil {
		return "no response"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "timeout"
	case strings.Contains(msg, "HTTP 404"):
		return "wrong endpoint (404)"
	}
	// Fallback: trim to a safe length.
	if len(msg) > 60 {
		msg = msg[:59] + "…"
	}
	return msg
}

// checkContext reports Phase 11 context-management readiness: threshold
// values in sane range, and whether the configured provider has a
// TokenCounter (without constructing one — that'd require API keys).
func checkContext(d *report, cfg *config.Config) {
	// Thresholds: must be 0 < soft < hard <= 1.
	soft, hard := cfg.Context.SoftThreshold, cfg.Context.HardThreshold
	switch {
	case soft <= 0 || soft >= 1:
		d.check("Context soft threshold",
			fmt.Sprintf("%.2f", soft),
			"must be in (0, 1) — defaults to 0.70 when unset",
			false)
	case hard <= 0 || hard > 1:
		d.check("Context hard threshold",
			fmt.Sprintf("%.2f", hard),
			"must be in (0, 1] — defaults to 0.90 when unset",
			false)
	case soft >= hard:
		d.check("Context thresholds",
			fmt.Sprintf("soft=%.2f hard=%.2f", soft, hard),
			"soft must be < hard",
			false)
	default:
		d.check("Context thresholds",
			fmt.Sprintf("soft=%.0f%% hard=%.0f%%", 100*soft, 100*hard),
			"ok",
			true)
	}

	// Token counter: every bundled provider satisfies agent.TokenCounter
	// (compile-time asserted in each provider's _test.go). The check here
	// is "is the configured provider one of the known-good names" rather
	// than a live probe — we don't want doctor to require API keys.
	known := map[string]bool{
		"anthropic":  true,
		"openai":     true,
		"google":     true,
		"gemini":     true,
		"ollama":     true,
		"llamacpp":   true,
		"vllm":       true,
		"lmstudio":   true,
		"litellm":    true,
		"groq":       true,
		"openrouter": true,
		"deepseek":   true,
		"xai":        true,
		"mistral":    true,
		"cerebras":   true,
	}
	if known[strings.ToLower(cfg.Defaults.Provider)] {
		d.check("Token counter",
			"supported by "+cfg.Defaults.Provider,
			"tiktoken / native — ctx% will be accurate",
			true)
	} else {
		d.check("Token counter",
			"unknown provider "+cfg.Defaults.Provider,
			"may not satisfy agent.TokenCounter — ctx% will stay at 0 until usage is reported",
			false)
	}
}
