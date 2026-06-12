package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse_SlashFrontmatter(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "bare slash field",
			src:  "---\nname: refactor\nslash: rf\n---\nbody",
			want: "rf",
		},
		{
			name: "leading-slash tolerated",
			src:  "---\nname: refactor\nslash: /rf\n---\nbody",
			want: "rf",
		},
		{
			name: "no slash field",
			src:  "---\nname: refactor\n---\nbody",
			want: "",
		},
		{
			name: "whitespace trimmed",
			src:  "---\nslash:   rf  \n---\nbody",
			want: "rf",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sk := parse(tc.src)
			if sk.Slash != tc.want {
				t.Fatalf("parse Slash = %q, want %q", sk.Slash, tc.want)
			}
		})
	}
}

func TestLoad_PopulatesSlashField(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review the diff\nslash: rv\n---\nLook at the working tree and review it."
	if err := os.WriteFile(filepath.Join(skillsDir, "review.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d skills, want 1", len(got))
	}
	if got[0].Slash != "rv" {
		t.Fatalf("Slash = %q, want rv", got[0].Slash)
	}
	if got[0].Name != "review" {
		t.Fatalf("Name = %q, want review", got[0].Name)
	}
}
