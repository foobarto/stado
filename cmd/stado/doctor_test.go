package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
