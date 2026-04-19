package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/providers/localdetect"
)

// TestRenderLocalRunnerHint_NoRunners returns empty when nothing is
// reachable. Caller uses "" to mean "no hint".
func TestRenderLocalRunnerHint_NoRunners(t *testing.T) {
	got := renderLocalRunnerHint([]localdetect.Result{
		{Name: "ollama", Reachable: false},
		{Name: "lmstudio", Reachable: false},
	})
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// TestRenderLocalRunnerHint_Multiple — most common real-world case:
// one runner up, others down. Verify the running one surfaces with its
// model list, and the down ones don't bleed into the hint.
func TestRenderLocalRunnerHint_Multiple(t *testing.T) {
	results := []localdetect.Result{
		{Name: "ollama", Endpoint: "http://localhost:11434/v1", Reachable: false},
		{Name: "lmstudio", Endpoint: "http://localhost:1234/v1", Reachable: true, Models: []string{"qwen-32b", "llama-70b"}},
		{Name: "vllm", Endpoint: "http://localhost:8000/v1", Reachable: false},
	}
	got := renderLocalRunnerHint(results)
	for _, want := range []string{
		"lmstudio",
		"localhost:1234",
		"2 models",
		"qwen-32b",
		"STADO_DEFAULTS_PROVIDER",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint missing %q:\n%s", want, got)
		}
	}
	// Down runners shouldn't leak in.
	for _, unwanted := range []string{"ollama", "vllm"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("hint should not mention down runner %q:\n%s", unwanted, got)
		}
	}
}

// TestRenderLocalRunnerHint_NoModelsLoaded — server up but empty.
func TestRenderLocalRunnerHint_NoModelsLoaded(t *testing.T) {
	got := renderLocalRunnerHint([]localdetect.Result{
		{Name: "lmstudio", Endpoint: "http://localhost:1234/v1", Reachable: true},
	})
	if !strings.Contains(got, "no models loaded") {
		t.Errorf("empty-model path missing advisory: %q", got)
	}
}

// TestRenderLocalRunnerHint_SingleModel uses singular wording ("1 model")
// — small UX detail but it reads wrong if it says "1 models".
func TestRenderLocalRunnerHint_SingleModel(t *testing.T) {
	got := renderLocalRunnerHint([]localdetect.Result{
		{Name: "ollama", Endpoint: "http://localhost:11434/v1", Reachable: true, Models: []string{"llama3.2:8b"}},
	})
	if !strings.Contains(got, "1 model: llama3.2:8b") {
		t.Errorf("singular wording missing: %q", got)
	}
	if strings.Contains(got, "1 models") {
		t.Errorf("plural wording leaked in singular case: %q", got)
	}
}
