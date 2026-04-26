package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginInit_WritesAllFiles: scaffolding produces the expected
// file set with the right content stamped in each.
func TestPluginInit_WritesAllFiles(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	pluginInitDir = ""
	pluginInitForce = false
	if err := pluginInitCmd.RunE(pluginInitCmd, []string{"my-plugin"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	root := filepath.Join(dir, "my-plugin")
	for _, f := range []string{
		"go.mod",
		"main.go",
		"plugin.manifest.template.json",
		"build.sh",
		"README.md",
	} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing scaffolded file %s: %v", f, err)
		}
	}
	// go.mod carries the plugin name as the module path.
	body, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	if !strings.Contains(string(body), "my-plugin") {
		t.Errorf("go.mod should reference the plugin name: %q", body)
	}
	// main.go exports the expected wasm ABI symbols.
	body, _ = os.ReadFile(filepath.Join(root, "main.go"))
	for _, want := range []string{"stado_alloc", "stado_free", "stado_tool_greet", "stadoLog"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("main.go missing %q: ...", want)
		}
	}
	// build.sh is executable.
	info, _ := os.Stat(filepath.Join(root, "build.sh"))
	if info.Mode()&0o100 == 0 {
		t.Errorf("build.sh not executable: %v", info.Mode())
	}
}

// TestPluginInit_DirFlagOverride: --dir lets the user place the
// scaffold somewhere other than ./<name>.
func TestPluginInit_DirFlagOverride(t *testing.T) {
	base := t.TempDir()
	restore := chdir(t, base)
	defer restore()

	custom := filepath.Join(base, "custom", "path")
	pluginInitDir = custom
	pluginInitForce = false
	defer func() { pluginInitDir = "" }()

	if err := pluginInitCmd.RunE(pluginInitCmd, []string{"plugname"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(custom, "main.go")); err != nil {
		t.Errorf("expected scaffold under --dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "plugname")); !os.IsNotExist(err) {
		t.Errorf("--dir should override default location: %v", err)
	}
}

// TestPluginInit_RejectsInvalidName: non-alphanum-dash names error.
func TestPluginInit_RejectsInvalidName(t *testing.T) {
	restore := chdir(t, t.TempDir())
	defer restore()

	pluginInitDir = ""
	pluginInitForce = false
	for _, bad := range []string{"Uppercase", "has space", "underscored_name", ""} {
		err := pluginInitCmd.RunE(pluginInitCmd, []string{bad})
		if err == nil {
			t.Errorf("expected error for invalid name %q", bad)
		}
	}
}

// TestPluginInit_RefusesExistingWithoutForce: second init into the
// same dir errors unless --force.
func TestPluginInit_RefusesExistingWithoutForce(t *testing.T) {
	base := t.TempDir()
	restore := chdir(t, base)
	defer restore()

	pluginInitDir = ""
	pluginInitForce = false
	if err := pluginInitCmd.RunE(pluginInitCmd, []string{"pl"}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	err := pluginInitCmd.RunE(pluginInitCmd, []string{"pl"})
	if err == nil {
		t.Fatal("second init should refuse without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention existing dir: %v", err)
	}
}

func TestPluginInitRejectsSymlinkDirWithForce(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	pluginInitDir = link
	pluginInitForce = true
	defer func() { pluginInitDir = ""; pluginInitForce = false }()

	err := pluginInitCmd.RunE(pluginInitCmd, []string{"pl"})
	if err == nil {
		t.Fatal("init should reject symlinked output dir")
	}
	if _, statErr := os.Stat(filepath.Join(target, "main.go")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}

func TestPluginInitRejectsSymlinkParentDir(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	pluginInitDir = filepath.Join(link, "plugin")
	pluginInitForce = false
	defer func() { pluginInitDir = ""; pluginInitForce = false }()

	err := pluginInitCmd.RunE(pluginInitCmd, []string{"pl"})
	if err == nil {
		t.Fatal("init should reject symlinked output parent dir")
	}
	if _, statErr := os.Stat(filepath.Join(target, "plugin")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}

func TestPluginInitRejectsScaffoldFileSymlinkWithForce(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "plugin")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(out, "decoy.sh")
	if err := os.WriteFile(decoy, []byte("do not replace"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("decoy.sh", filepath.Join(out, "build.sh")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	pluginInitDir = out
	pluginInitForce = true
	defer func() { pluginInitDir = ""; pluginInitForce = false }()

	err := pluginInitCmd.RunE(pluginInitCmd, []string{"pl"})
	if err == nil {
		t.Fatal("init should reject symlinked scaffold file")
	}
	data, readErr := os.ReadFile(decoy)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "do not replace" {
		t.Fatalf("symlink target modified: %q", data)
	}
}
