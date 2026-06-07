package plugins

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListInstalledDirsStreamsAndSortsDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta-1.0.0", "alpha-1.0.0"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-dir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListInstalledDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha-1.0.0", "zeta-1.0.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListInstalledDirs = %v, want %v", got, want)
	}
}

func TestListInstalledDirsSkipsActiveMarkerDir(t *testing.T) {
	// PinActiveDev writes <plugins>/active/<name> markers. That reserved dir
	// is not an installed plugin; enumerating it produced a phantom "active"
	// row that failed manifest load and inflated the installed count.
	root := t.TempDir()
	for _, name := range []string{"hello-0.1.0", activeMarkerDir} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListInstalledDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"hello-0.1.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListInstalledDirs = %v, want %v (the %q marker dir must be excluded)", got, want, activeMarkerDir)
	}
}

func TestListInstalledDirsRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a-1.0.0", "b-1.0.0", "c-1.0.0"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := listInstalledDirs(root, 2)
	if err == nil {
		t.Fatal("listInstalledDirs succeeded with too many entries")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Fatalf("listInstalledDirs error = %v, want entry cap", err)
	}
}

func TestListInstalledDirsSkipsSymlinkedPluginDirs(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-1.0.0")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got, err := ListInstalledDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "target" {
		t.Fatalf("ListInstalledDirs = %v, want only real target directory", got)
	}
}
