package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/textutil"
)

var (
	doctorJSON    bool
	doctorNoLocal bool
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
		d := buildDoctorReport(cmd.Context(), cfg, doctorOptions{
			json:    doctorJSON,
			noLocal: doctorNoLocal,
		})
		if doctorJSON {
			d.renderJSON(cmd.OutOrStdout())
		} else {
			d.render(cmd.OutOrStdout())
		}
		if d.fails > 0 {
			os.Exit(1)
		}
		return nil
	},
}

type doctorOptions struct {
	json    bool
	noLocal bool
}

var checkLocalProvidersFn = checkLocalProviders

func buildDoctorReport(ctx context.Context, cfg *config.Config, opts doctorOptions) report {
	var d report
	d.check("OS/arch", runtime.GOOS+"/"+runtime.GOARCH, "ok", true)
	d.check("Go runtime", runtime.Version(), "ok", true)

	// Paths.
	d.check("Config file", cfg.ConfigPath, statusOfPath(cfg.ConfigPath), true)
	d.check("State dir", cfg.StateDir(), statusOfPath(cfg.StateDir()), true)
	d.check("Worktree dir", cfg.WorktreeDir(), statusOfPath(cfg.WorktreeDir()), true)

	// Provider selection + API key check.
	prov := cfg.Defaults.Provider
	if prov == "" {
		d.check("Provider", "(unset — probes local at boot)",
			"stado will auto-detect a running local runner (ollama/lmstudio/llamacpp/vllm/user preset); set defaults.provider in config to pin a specific provider",
			true)
	} else {
		d.check("Provider", prov, "configured", true)
		if keyEnv := providerEnvName(prov); keyEnv != "" {
			if os.Getenv(keyEnv) != "" {
				d.check("Provider key  ("+keyEnv+")", "present", "ok", true)
			} else {
				d.check("Provider key  ("+keyEnv+")", "not set", "missing — stado will fail on first prompt", false)
			}
		}
	}

	// External tools — PATH or fallback notes.
	d.checkBin("ripgrep (rg)", "rg")
	d.checkBin("ast-grep", "ast-grep")
	d.checkBin("bubblewrap (bwrap)", "bwrap")
	d.checkOptionalBin("gopls", "gopls",
		"optional — install via `go install golang.org/x/tools/gopls@latest` to enable the lsp-find tool")
	d.checkBin("git", "git")
	d.checkBin("cosign", "cosign")

	// Sandbox detection.
	d.check("Sandbox runner", sandbox.Detect().Name(), "ok", true)
	if err := sandbox.ProbeLandlock(); err == nil {
		d.check("Landlock", "available", "kernel ≥ 5.13", true)
	} else {
		d.check("Landlock", err.Error(), "unavailable (not fatal)", false)
	}

	// Context management readiness (Phase 11).
	checkContext(&d, cfg)

	// Opt-in feature visibility — so a user who wrote config.toml
	// can confirm the knob took effect. These are all ✓ with
	// value = "(unset)" when absent; the point is to show the
	// feature exists rather than gate on it being configured.
	checkOptInFeatures(&d, cfg)

	// Local inference autodetection — probe ollama / llamacpp /
	// vllm / lmstudio endpoints so the report tells the user
	// "you have lmstudio running at localhost:1234 with 3 models
	// loaded" without requiring them to set up a provider first.
	// Merges in user-configured presets at local-looking endpoints
	// so custom ports get probed too.
	//
	// Skip the probe when the user has pinned [defaults].provider
	// to a remote provider (anthropic, openai, gemini, or an OAI-compat
	// preset whose resolved endpoint is non-local). Saves the ~4s of
	// TCP timeouts on machines with no local runners — dogfood note
	// from htb-writeups workflow integration. Explicit --no-local still
	// works as before.
	skipLocal, skipReason := shouldSkipLocalProbe(cfg)
	switch {
	case opts.noLocal:
		// honour the explicit flag without annotation
	case skipLocal:
		d.check("Local probe", "skipped", skipReason, true)
	default:
		checkLocalProvidersFn(ctx, &d, cfg)
	}

	return d
}

// shouldSkipLocalProbe reports whether buildDoctorReport should skip
// the local-runner probe based on the configured [defaults].provider.
// Returns (true, reason) when the configured provider points away from
// any localhost endpoint. Empty provider, local-runner names, and
// presets whose endpoint resolves to localhost return (false, "").
func shouldSkipLocalProbe(cfg *config.Config) (bool, string) {
	if cfg == nil {
		return false, ""
	}
	name := strings.TrimSpace(cfg.Defaults.Provider)
	if name == "" {
		return false, ""
	}

	// Provider-direct names — always remote.
	switch strings.ToLower(name) {
	case "anthropic", "openai", "google", "gemini":
		return true, "[defaults].provider=" + name + " is a remote provider"
	}

	// OAI-compat preset: resolve user override first, then builtin default.
	endpoint := ""
	if cfg.Inference.Presets != nil {
		if p, ok := cfg.Inference.Presets[name]; ok {
			endpoint = p.Endpoint
		}
	}
	if endpoint == "" {
		if ep, _, ok := config.BuiltinInferencePreset(name); ok {
			endpoint = ep
		}
	}
	if endpoint == "" {
		// Unknown preset — leave the probe on so the user sees
		// any local runners that might match later config.
		return false, ""
	}
	if localdetect.IsLocalEndpoint(endpoint) {
		return false, ""
	}
	return true, "[defaults].provider=" + name + " (endpoint " + endpoint + ") is non-local"
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

// checkOptionalBin is checkBin that doesn't flag the report as failed
// when the binary is missing. Used for capabilities like gopls where
// stado functions fine without them (the LSP tool silently skips) —
// the user should still see the row but "1 check failed" shouldn't
// be triggered by a missing optional dependency.
func (r *report) checkOptionalBin(label, bin, note string) {
	full, err := exec.LookPath(bin)
	if err != nil {
		// Emit a visually-distinct row but don't bump r.fails.
		r.rows = append(r.rows, reportRow{
			label:  label,
			value:  "not found on PATH",
			detail: note,
			ok:     true, // render as ✓ so the exit code stays clean
		})
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
		noun := "checks"
		if r.fails == 1 {
			noun = "check"
		}
		fmt.Fprintf(w, "\n%d %s failed\n", r.fails, noun)
	} else {
		fmt.Fprintln(w, "\nall checks passed")
	}
}

func (r *report) renderJSON(w fmtWriter) {
	enc := json.NewEncoder(w)
	for _, row := range r.rows {
		status := "ok"
		if !row.ok {
			status = "error"
		}
		_ = enc.Encode(map[string]string{
			"check":  row.label,
			"status": status,
			"value":  row.value,
			"detail": row.detail,
		})
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
	return config.ProviderAPIKeyEnv(provider)
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Emit one JSON object per check")
	doctorCmd.Flags().BoolVar(&doctorNoLocal, "no-local", false, "Skip local-runner probes")
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
		d.check(label, r.Endpoint, localProviderDetail(r), true)
	}
}

func localProviderDetail(r localdetect.Result) string {
	models := r.RunnableModels()
	switch {
	case !r.Reachable:
		return "not running (probe: " + sanitizeErr(r.Err) + ")"
	case r.LoadStateKnown && len(models) == 0:
		return fmt.Sprintf("running · %d installed model(s), none loaded — load one in LM Studio or run `lms load <model>`", len(r.Models))
	case len(models) == 0:
		return "running · no models loaded"
	default:
		return fmt.Sprintf("running · %d model(s): %s", len(models), modelPreview(models))
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

func modelPreview(models []string) string {
	preview := textutil.StripControlChars(strings.Join(models, ", "))
	if len(preview) > 80 {
		preview = preview[:79] + "…"
	}
	return preview
}

// checkOptInFeatures surfaces the user-opt-in config surfaces so
// `stado doctor` doubles as a "did my config.toml take effect?"
// report. Each row is ✓ regardless of setting — we just want the
// value to be visible. Users often forget which knobs exist.
func checkOptInFeatures(d *report, cfg *config.Config) {
	// [budget]
	budgetVal := "(unset — no cost guardrail)"
	if cfg.Budget.WarnUSD > 0 || cfg.Budget.HardUSD > 0 {
		w := "(unset)"
		if cfg.Budget.WarnUSD > 0 {
			w = fmt.Sprintf("$%.2f", cfg.Budget.WarnUSD)
		}
		h := "(unset)"
		if cfg.Budget.HardUSD > 0 {
			h = fmt.Sprintf("$%.2f", cfg.Budget.HardUSD)
		}
		budgetVal = fmt.Sprintf("warn=%s hard=%s", w, h)
	}
	d.check("Budget caps", budgetVal, "ok", true)

	// [hooks]
	hooksVal := "(unset)"
	if cfg.Hooks.PostTurn != "" {
		cmd := cfg.Hooks.PostTurn
		if len(cmd) > 40 {
			cmd = cmd[:37] + "..."
		}
		hooksVal = "post_turn: " + cmd
	}
	d.check("Lifecycle hooks", hooksVal, "ok", true)

	// [tools]
	toolsVal := "(default — all bundled tools available)"
	if len(cfg.Tools.Enabled) > 0 {
		toolsVal = fmt.Sprintf("allowlist: %s", strings.Join(cfg.Tools.Enabled, ","))
	} else if len(cfg.Tools.Disabled) > 0 {
		toolsVal = fmt.Sprintf("defaults minus: %s", strings.Join(cfg.Tools.Disabled, ","))
	}
	d.check("Tools filter", toolsVal, "ok", true)
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
	switch {
	case cfg.Defaults.Provider == "":
		d.check("Token counter",
			"provider resolved at boot via local probe",
			"ctx% accuracy depends on which local runner answers — typically works with ollama/lmstudio/llamacpp/vllm",
			true)
	case known[strings.ToLower(cfg.Defaults.Provider)]:
		d.check("Token counter",
			"supported by "+cfg.Defaults.Provider,
			"tiktoken / native — ctx% will be accurate",
			true)
	default:
		d.check("Token counter",
			"unknown provider "+cfg.Defaults.Provider,
			"may not satisfy agent.TokenCounter — ctx% will stay at 0 until usage is reported",
			false)
	}
}
