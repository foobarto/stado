package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_AgentsMdWins: when both AGENTS.md and CLAUDE.md exist in
// the same directory, AGENTS.md is picked. This matches the
// emerging cross-vendor convention; CLAUDE.md stays supported for
// backwards compatibility with existing repos.
func TestLoad_AgentsMdWins(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "AGENTS.md"), "agents body")
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "claude body")

	r, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "agents body" {
		t.Errorf("expected AGENTS.md to win; got %q", r.Content)
	}
	if !strings.HasSuffix(r.Path, "AGENTS.md") {
		t.Errorf("expected path to end in AGENTS.md; got %q", r.Path)
	}
}

// TestLoad_ClaudeMdFallback: with only CLAUDE.md present, it loads.
func TestLoad_ClaudeMdFallback(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "legacy body")

	r, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "legacy body" {
		t.Errorf("fallback path didn't read CLAUDE.md; got %q", r.Content)
	}
}

// TestLoad_WalksUp: invocation from a subdirectory of the repo still
// finds the file. This is the common real-world case — a user
// launches stado from deep inside the tree.
func TestLoad_WalksUp(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root rules")
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Load(sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "root rules" {
		t.Errorf("walk-up didn't find root AGENTS.md; got %q", r.Content)
	}
}

// TestLoad_NoFileIsNotAnError: clean miss returns empty result with
// no error. Callers use Content=="" as the "no instructions" signal
// without special-casing an error.
func TestLoad_NoFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("miss should not error; got %v", err)
	}
	if r.Content != "" || r.Path != "" {
		t.Errorf("expected empty result, got %+v", r)
	}
}

// TestLoad_NearestWins: when a repo has AGENTS.md at multiple levels
// (monorepo), the closest one wins — tighter context beats wider.
func TestLoad_NearestWins(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root")
	sub := filepath.Join(root, "pkg", "mod")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sub, "AGENTS.md"), "module-local")

	r, err := Load(sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "module-local" {
		t.Errorf("nearest-wins failed; got %q", r.Content)
	}
}

func TestLoad_SkipsSymlinkedInstructions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.txt")
	mustWrite(t, target, "secret")
	link := filepath.Join(dir, "AGENTS.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	r, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Path != "" || r.Content != "" {
		t.Fatalf("expected symlinked instructions to be skipped, got %+v", r)
	}
}

func TestComposeSystemPrompt_AddsStadoIdentityAndRuntime(t *testing.T) {
	got := ComposeSystemPrompt(DefaultSystemPromptTemplate, "always write tests", RuntimeContext{
		Provider: "lmstudio",
		Model:    "qwen/qwen3.6-35b-a3b",
	})
	for _, want := range []string{
		"Identify as stado",
		"Do not claim to be Claude Code",
		"provider: lmstudio",
		"model: qwen/qwen3.6-35b-a3b",
		"Cairn workflow defaults:",
		"think before coding, simplicity first, surgical changes, and goal-driven execution",
		"Project instructions:\nalways write tests",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composed prompt missing %q\n%s", want, got)
		}
	}
}

func TestComposeSystemPrompt_AlwaysIncludesIdentity(t *testing.T) {
	got := ComposeSystemPrompt("", "", RuntimeContext{})
	if !strings.Contains(got, "Identify as stado") {
		t.Fatalf("base prompt missing stado identity\n%s", got)
	}
	if strings.Contains(got, "Project instructions:") {
		t.Fatalf("empty project prompt should not add project section\n%s", got)
	}
}

func TestComposeSystemPrompt_ExecutesCustomTemplate(t *testing.T) {
	got := ComposeSystemPrompt(
		`agent={{ .Model }} via {{ .Provider }} rules={{ .ProjectInstructions }}`,
		"be direct",
		RuntimeContext{Provider: "lmstudio", Model: "qwen"},
	)
	if got != "agent=qwen via lmstudio rules=be direct" {
		t.Fatalf("custom template output = %q", got)
	}
}

func TestComposeSystemPrompt_AppendsMemoryContext(t *testing.T) {
	got := ComposeSystemPrompt(
		`agent={{ .Model }} rules={{ .ProjectInstructions }}`,
		"be direct",
		RuntimeContext{Model: "qwen", Memory: "Memory snippets supplied by installed plugins.\n- [global/preference mem_1] Prefer focused tests."},
	)
	for _, want := range []string{
		"agent=qwen rules=be direct",
		"Memory snippets supplied by installed plugins.",
		"[global/preference mem_1]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composed prompt missing %q:\n%s", want, got)
		}
	}
}

func TestValidateSystemPromptTemplateRejectsUnknownFields(t *testing.T) {
	if err := ValidateSystemPromptTemplate(`{{ .Unknown }}`); err == nil {
		t.Fatal("expected unknown template field to fail validation")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
