package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_SkipsSymlinkedSkillFiles(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(skillsDir, "exfil.md")); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected symlinked skill to be skipped, got %+v", got)
	}
}
