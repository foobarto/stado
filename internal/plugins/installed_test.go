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
	for _, name := range []string{"remote-" + strings.Repeat("f", 64), "local-" + strings.Repeat("0", 64)} {
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
	want := []string{"local-" + strings.Repeat("0", 64), "remote-" + strings.Repeat("f", 64)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListInstalledDirs = %v, want %v", got, want)
	}
}

func TestListInstalledDirsSkipsActiveMarkerDir(t *testing.T) {
	// PinActiveDev writes <plugins>/active/<name> markers. That reserved dir
	// is not an installed plugin; enumerating it produced a phantom "active"
	// row that failed manifest load and inflated the installed count.
	root := t.TempDir()
	storeKey := "local-" + strings.Repeat("1", 64)
	for _, name := range []string{storeKey, activeMarkerDir} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListInstalledDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{storeKey}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListInstalledDirs = %v, want %v (the %q marker dir must be excluded)", got, want, activeMarkerDir)
	}
}

func TestListInstalledDirsRejectsRetiredAnchorMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "anchor-trust"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ListInstalledDirs(root); err == nil || !strings.Contains(err.Error(), "retired pre-v0.80 anchor metadata") {
		t.Fatalf("legacy anchor metadata error = %v", err)
	}
}

func TestListInstalledDirsRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"local-" + strings.Repeat("a", 64), "local-" + strings.Repeat("b", 64), "local-" + strings.Repeat("c", 64)} {
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

func TestListInstalledDirsRejectsSymlinkedPluginDirs(t *testing.T) {
	root := t.TempDir()
	targetName := "local-" + strings.Repeat("2", 64)
	target := filepath.Join(root, targetName)
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "local-"+strings.Repeat("3", 64))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, err := ListInstalledDirs(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ListInstalledDirs symlink error = %v", err)
	}
}
