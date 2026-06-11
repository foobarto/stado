package changelog

import (
	"os"
	"strings"
	"testing"
)

// Drift guard: latest.md must equal the first "## v" section of the repo-root
// CHANGELOG.md. If this fails, run `make` to regenerate it.
func TestLatestMatchesRootChangelog(t *testing.T) {
	root, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read root CHANGELOG: %v", err)
	}
	if strings.TrimSpace(latest) != strings.TrimSpace(firstSection(string(root))) {
		t.Fatal("internal/changelog/latest.md is stale — run `make` to regenerate it from CHANGELOG.md")
	}
}

func firstSection(s string) string {
	var b strings.Builder
	count := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "## v") {
			count++
			if count > 1 {
				break
			}
		}
		if count == 1 {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestLatestParses(t *testing.T) {
	r := Latest()
	if !strings.HasPrefix(r.Version, "v") {
		t.Errorf("version = %q, want a v-prefixed tag", r.Version)
	}
	if r.Title == "" {
		t.Error("title should be non-empty")
	}
	if len(r.Highlights) == 0 {
		t.Error("expected at least one highlight")
	}
}
