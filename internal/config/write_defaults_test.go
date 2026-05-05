package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDefaultsCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := WriteDefaults(path, "lmstudio", "qwen"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`[defaults]`, `provider = "lmstudio"`, `model = "qwen"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteDefaultsPreservesUnspecifiedProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nprovider = \"anthropic\"\nmodel = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteDefaults(path, "", "new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`provider = "anthropic"`, `model = "new"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteTUIThinkingDisplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nprovider = \"anthropic\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteTUIThinkingDisplay(path, "hide"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`provider = "anthropic"`, `[tui]`, `thinking_display = "hide"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteTUITheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := WriteTUITheme(path, "stado-rose"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`[tui]`, `theme = "stado-rose"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteDefaultsRejectsConfigSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(outside, []byte("[defaults]\nprovider = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := WriteDefaults(path, "openai", "gpt-test"); err == nil {
		t.Fatal("WriteDefaults should reject symlink escape")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "openai") {
		t.Fatalf("outside config was modified:\n%s", data)
	}
}

func TestWriteDefaultsRejectsInRootConfigSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy.toml")
	if err := os.WriteFile(decoy, []byte("[defaults]\nprovider = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.Symlink("decoy.toml", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := WriteDefaults(path, "openai", "gpt-test"); err == nil {
		t.Fatal("WriteDefaults should reject in-root config symlink")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "openai") {
		t.Fatalf("in-root symlink target was modified:\n%s", data)
	}
}

func TestWriteDefaultsRejectsConfigParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "config-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	path := filepath.Join(link, "config.toml")

	if err := WriteDefaults(path, "openai", "gpt-test"); err == nil {
		t.Fatal("WriteDefaults should reject symlinked config parent dirs")
	}
	if _, err := os.Stat(filepath.Join(target, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified, stat err = %v", err)
	}
}

func TestWriteToolsListAddCreatesAndDedupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := WriteToolsListAdd(path, "autoload", []string{"fs.read", "bash"}); err != nil {
		t.Fatalf("add #1: %v", err)
	}
	if err := WriteToolsListAdd(path, "autoload", []string{"bash", "fs.write"}); err != nil {
		t.Fatalf("add #2: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"fs.read", "bash", "fs.write"} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q: %s", want, got)
		}
	}
	if strings.Count(got, `"bash"`) != 1 {
		t.Errorf("bash should appear once after dedupe:\n%s", got)
	}
}

func TestWriteToolsListRemoveDropsEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := WriteToolsListAdd(path, "disabled", []string{"webfetch", "ripgrep"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteToolsListRemove(path, "disabled", []string{"webfetch"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "webfetch") {
		t.Errorf("webfetch should have been removed:\n%s", got)
	}
	if !strings.Contains(got, "ripgrep") {
		t.Errorf("ripgrep should remain:\n%s", got)
	}
}

func TestWriteToolsListRejectsUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteToolsListAdd(path, "bogus", []string{"x"}); err == nil {
		t.Fatal("expected error for unknown [tools] key")
	}
}
