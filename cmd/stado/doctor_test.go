package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestProviderEnvName_KnownAndUnknown(t *testing.T) {
	cases := map[string]string{
		"anthropic":  "ANTHROPIC_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"google":     "GEMINI_API_KEY",
		"gemini":     "GEMINI_API_KEY",
		"groq":       "GROQ_API_KEY",
		"openrouter": "OPENROUTER_API_KEY",
		"deepseek":   "DEEPSEEK_API_KEY",
		"xai":        "XAI_API_KEY",
		"mistral":    "MISTRAL_API_KEY",
		"cerebras":   "CEREBRAS_API_KEY",
		"litellm":    "LITELLM_API_KEY",
		"ollama":     "", // local, no key
		"unknown":    "",
	}
	for provider, want := range cases {
		if got := providerEnvName(provider); got != want {
			t.Errorf("providerEnvName(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestStatusOfPath_MissingVsPresent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	if got := statusOfPath(missing); !strings.Contains(got, "does not exist") {
		t.Errorf("missing path status = %q", got)
	}
	present := filepath.Join(t.TempDir(), "there")
	os.Mkdir(present, 0o755)
	if got := statusOfPath(present); !strings.Contains(got, "dir") {
		t.Errorf("dir status = %q", got)
	}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, []byte("x"), 0o644)
	if got := statusOfPath(file); !strings.Contains(got, "file") {
		t.Errorf("file status = %q", got)
	}
}

func TestReport_Render(t *testing.T) {
	r := &report{}
	r.check("label-a", "value-a", "detail-a", true)
	r.check("label-b-longer", "value-b", "detail-b", false)

	var buf strings.Builder
	r.render(&buf)
	out := buf.String()
	if !strings.Contains(out, "✓") || !strings.Contains(out, "✗") {
		t.Errorf("missing status marks: %q", out)
	}
	if !strings.Contains(out, "1 check failed") {
		t.Errorf("failure count missing (singular): %q", out)
	}
	if r.fails != 1 {
		t.Errorf("fails = %d, want 1", r.fails)
	}
	// Second failing check should switch to plural.
	r.check("label-c", "value-c", "detail-c", false)
	var buf2 strings.Builder
	r.render(&buf2)
	if !strings.Contains(buf2.String(), "2 checks failed") {
		t.Errorf("failure count missing (plural): %q", buf2.String())
	}
}

func TestReport_AllPassedMessage(t *testing.T) {
	r := &report{}
	r.check("one", "v", "ok", true)
	var buf strings.Builder
	r.render(&buf)
	if !strings.Contains(buf.String(), "all checks passed") {
		t.Errorf("all-passed message missing: %q", buf.String())
	}
}

// TestCheckOptionalBin_MissingDoesNotFail: missing optional dep
// renders as a visible row but does NOT bump report.fails. Before
// this split, `stado doctor` exited 2 on any dev machine without
// gopls installed — noise, since stado works fine without it.
func TestCheckOptionalBin_MissingDoesNotFail(t *testing.T) {
	r := &report{}
	r.checkOptionalBin("definitely-optional", "probably-not-on-path-xyzzy", "test note")
	if r.fails != 0 {
		t.Errorf("expected fails=0 for missing optional bin, got %d", r.fails)
	}
	if len(r.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(r.rows))
	}
	if !r.rows[0].ok {
		t.Error("expected row to render as ok (✓) even though bin is missing")
	}
	if r.rows[0].detail != "test note" {
		t.Errorf("expected detail to carry note; got %q", r.rows[0].detail)
	}
}

// TestCheckBin_MissingDoesFail: the non-optional form still counts
// as a real failure. Guards against a refactor collapsing the two.
func TestCheckBin_MissingDoesFail(t *testing.T) {
	r := &report{}
	r.checkBin("required-tool", "probably-not-on-path-xyzzy")
	if r.fails != 1 {
		t.Errorf("expected fails=1 for missing required bin, got %d", r.fails)
	}
}

// TestCheckOptInFeatures_SurfacesConfiguredAndUnset: verifies the
// "did my config take effect?" rows render whether the knob is set
// or not. Users often forget which features exist; the doctor output
// doubles as a discovery hint.
func TestCheckOptInFeatures_SurfacesConfiguredAndUnset(t *testing.T) {
	// With everything unset: three rows, all ✓, values showing "unset".
	r := &report{}
	cfg := &config.Config{}
	checkOptInFeatures(r, cfg)
	if len(r.rows) != 3 {
		t.Fatalf("expected 3 rows (budget/hooks/tools); got %d", len(r.rows))
	}
	for _, row := range r.rows {
		if !row.ok {
			t.Errorf("row %q should be ok (informational)", row.label)
		}
	}
	// With budget + hooks configured, the values must reflect that.
	r2 := &report{}
	cfg2 := &config.Config{}
	cfg2.Budget.WarnUSD = 1.0
	cfg2.Budget.HardUSD = 5.0
	cfg2.Hooks.PostTurn = "notify-send 'done'"
	cfg2.Tools.Disabled = []string{"webfetch"}
	checkOptInFeatures(r2, cfg2)
	// Concatenate all row values so assertion order is stable.
	var all strings.Builder
	for _, row := range r2.rows {
		all.WriteString(row.label + "=" + row.value + ";")
	}
	got := all.String()
	for _, want := range []string{"$1.00", "$5.00", "notify-send", "webfetch"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rows; got %q", want, got)
		}
	}
}

func TestReport_RenderJSON(t *testing.T) {
	r := &report{}
	r.check("ripgrep (rg)", "/usr/bin/rg", "ok", true)
	r.check("Provider key", "not set", "missing", false)

	var buf strings.Builder
	r.renderJSON(&buf)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON rows, got %d: %q", len(lines), buf.String())
	}

	var row1 struct {
		Check  string `json:"check"`
		Status string `json:"status"`
		Value  string `json:"value"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &row1); err != nil {
		t.Fatalf("row 1 is not valid JSON: %v", err)
	}
	if row1.Check != "ripgrep (rg)" || row1.Status != "ok" || row1.Value != "/usr/bin/rg" || row1.Detail != "ok" {
		t.Fatalf("unexpected row 1: %+v", row1)
	}

	var row2 struct {
		Check  string `json:"check"`
		Status string `json:"status"`
		Value  string `json:"value"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &row2); err != nil {
		t.Fatalf("row 2 is not valid JSON: %v", err)
	}
	if row2.Check != "Provider key" || row2.Status != "error" || row2.Value != "not set" || row2.Detail != "missing" {
		t.Fatalf("unexpected row 2: %+v", row2)
	}
}

func TestDoctorFlags_Registered(t *testing.T) {
	for _, name := range []string{"json", "no-local"} {
		if doctorCmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected doctor flag %q to be registered", name)
		}
	}
}

func TestBuildDoctorReport_NoLocalSkipsLocalProbe(t *testing.T) {
	prev := checkLocalProvidersFn
	defer func() { checkLocalProvidersFn = prev }()

	called := 0
	checkLocalProvidersFn = func(ctx context.Context, d *report, cfg *config.Config) {
		called++
	}

	_ = buildDoctorReport(context.Background(), &config.Config{}, doctorOptions{noLocal: true})
	if called != 0 {
		t.Fatalf("expected no-local report build to skip local provider probe, called=%d", called)
	}
}

func TestShouldSkipLocalProbe(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *config.Config
		wantSkip bool
	}{
		{
			name:     "empty cfg → don't skip (probe is informational)",
			cfg:      &config.Config{},
			wantSkip: false,
		},
		{
			name: "anthropic pinned → skip",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "anthropic"},
			},
			wantSkip: true,
		},
		{
			name: "openai pinned → skip",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "openai"},
			},
			wantSkip: true,
		},
		{
			name: "ollama pinned → don't skip (it IS local)",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "ollama"},
			},
			wantSkip: false,
		},
		{
			name: "litellm with default localhost endpoint → don't skip",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "litellm"},
			},
			wantSkip: false,
		},
		{
			name: "litellm overridden to remote → skip",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "litellm"},
				Inference: config.Inference{
					Presets: map[string]config.InferencePreset{
						"litellm": {Endpoint: "https://ollama.com/v1"},
					},
				},
			},
			wantSkip: true,
		},
		{
			name: "custom preset pointing at remote → skip",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "ollama-cloud"},
				Inference: config.Inference{
					Presets: map[string]config.InferencePreset{
						"ollama-cloud": {Endpoint: "https://ollama.com/v1"},
					},
				},
			},
			wantSkip: true,
		},
		{
			name: "custom preset pointing at localhost → don't skip",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "my-local"},
				Inference: config.Inference{
					Presets: map[string]config.InferencePreset{
						"my-local": {Endpoint: "http://localhost:9999/v1"},
					},
				},
			},
			wantSkip: false,
		},
		{
			name: "unknown preset, no override → don't skip (let probe surface what's there)",
			cfg: &config.Config{
				Defaults: config.Defaults{Provider: "made-up-name"},
			},
			wantSkip: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotSkip, gotReason := shouldSkipLocalProbe(c.cfg)
			if gotSkip != c.wantSkip {
				t.Errorf("shouldSkipLocalProbe = (%v, %q), want skip=%v",
					gotSkip, gotReason, c.wantSkip)
			}
			if gotSkip && gotReason == "" {
				t.Errorf("expected non-empty reason when skipping, got empty")
			}
		})
	}
}

func TestBuildDoctorReport_PinnedRemoteSkipsLocalProbe(t *testing.T) {
	prev := checkLocalProvidersFn
	defer func() { checkLocalProvidersFn = prev }()

	called := 0
	checkLocalProvidersFn = func(ctx context.Context, d *report, cfg *config.Config) {
		called++
	}

	cfg := &config.Config{Defaults: config.Defaults{Provider: "anthropic"}}
	d := buildDoctorReport(context.Background(), cfg, doctorOptions{})

	if called != 0 {
		t.Fatalf("expected pinned-remote provider to skip local probe, called=%d", called)
	}

	// And a "Local probe: skipped" annotation row should be emitted so
	// the operator can see why the probe didn't run.
	found := false
	for _, row := range d.rows {
		if row.label == "Local probe" && row.value == "skipped" {
			found = true
			if !strings.Contains(row.detail, "anthropic") {
				t.Errorf("expected skip reason to mention provider name, got %q", row.detail)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected a 'Local probe: skipped' annotation row, none found")
	}
}
