package archguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GoReleaser creates dist/ metadata before compiling. If that generated tree
// is not ignored, Go's -buildvcs=true stamping reports an otherwise clean
// tagged release as modified.
func TestGoReleaserDistIsIgnored(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "/dist/" {
			return
		}
	}
	t.Fatal(".gitignore must contain /dist/ so release build-info stays clean")
}
