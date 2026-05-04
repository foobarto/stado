package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLine_BasicForms(t *testing.T) {
	cases := []struct {
		in        string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{"FOO=bar", "FOO", "bar", true},
		{"  FOO=bar", "FOO", "bar", true},
		{"FOO=  bar  ", "FOO", "bar", true},
		{`FOO="value with spaces"`, "FOO", "value with spaces", true},
		{`FOO='single quoted'`, "FOO", "single quoted", true},
		{`FOO="quoted" # trailing comment`, "FOO", "quoted", true},
		{"FOO=unquoted # trailing comment", "FOO", "unquoted", true},
		{"export FOO=bar", "FOO", "bar", true},
		{"# whole-line comment", "", "", false},
		{"   ", "", "", false},
		{"", "", "", false},
		{"=novalue", "", "", false},
		{"1FOO=bad", "", "", false},
		{"FOO BAR=bad", "", "", false},
		{"FOO_BAR=ok", "FOO_BAR", "ok", true},
		{"foo_bar=ok", "foo_bar", "ok", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			k, v, ok := parseLine(tc.in)
			if ok != tc.wantOK || k != tc.wantKey || v != tc.wantValue {
				t.Errorf("parseLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, k, v, ok, tc.wantKey, tc.wantValue, tc.wantOK)
			}
		})
	}
}

func TestLoadHierarchy_FindsCwdEnvFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("STADO_DOTENV_TEST_A=ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STADO_DOTENV_TEST_A", "")
	_ = os.Unsetenv("STADO_DOTENV_TEST_A") // ensure we test the unset path
	applied := LoadHierarchy(dir)
	if len(applied) != 1 {
		t.Fatalf("applied = %v, want one .env file", applied)
	}
	if got := os.Getenv("STADO_DOTENV_TEST_A"); got != "ok" {
		t.Errorf("env not applied: STADO_DOTENV_TEST_A = %q", got)
	}
	_ = os.Unsetenv("STADO_DOTENV_TEST_A")
}

func TestLoadHierarchy_WalksUpToParents(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Parent's .env defines KEY_A. Closer .env is absent here.
	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte("STADO_DOTENV_TEST_B=parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("STADO_DOTENV_TEST_B")
	applied := LoadHierarchy(sub)
	if len(applied) != 1 {
		t.Fatalf("applied = %v, want exactly one parent .env", applied)
	}
	if got := os.Getenv("STADO_DOTENV_TEST_B"); got != "parent" {
		t.Errorf("parent .env value not applied: %q", got)
	}
	_ = os.Unsetenv("STADO_DOTENV_TEST_B")
}

func TestLoadHierarchy_CloserFileWinsOverParent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte("STADO_DOTENV_TEST_C=parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".env"),
		[]byte("STADO_DOTENV_TEST_C=closer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("STADO_DOTENV_TEST_C")
	LoadHierarchy(sub)
	if got := os.Getenv("STADO_DOTENV_TEST_C"); got != "closer" {
		t.Errorf("closer file did NOT win: %q (want 'closer')", got)
	}
	_ = os.Unsetenv("STADO_DOTENV_TEST_C")
}

func TestLoadHierarchy_DoesNotOverwriteExistingEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("STADO_DOTENV_TEST_D=from_dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STADO_DOTENV_TEST_D", "from_shell")
	LoadHierarchy(dir)
	if got := os.Getenv("STADO_DOTENV_TEST_D"); got != "from_shell" {
		t.Errorf("dotenv overwrote shell env: %q (want 'from_shell')", got)
	}
}

func TestLoadHierarchy_HandlesMissingDir(t *testing.T) {
	applied := LoadHierarchy("/this/does/not/exist/anywhere")
	if applied != nil {
		t.Errorf("expected nil for missing dir, got %v", applied)
	}
}

func TestLoadHierarchy_IgnoresMalformedLines(t *testing.T) {
	dir := t.TempDir()
	body := `# comment
GOOD=value
=novalue
BAD KEY=foo
1NUMSTART=foo
ANOTHER_GOOD="quoted value"
`
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("GOOD")
	_ = os.Unsetenv("ANOTHER_GOOD")
	LoadHierarchy(dir)
	if os.Getenv("GOOD") != "value" {
		t.Errorf("GOOD = %q", os.Getenv("GOOD"))
	}
	if os.Getenv("ANOTHER_GOOD") != "quoted value" {
		t.Errorf("ANOTHER_GOOD = %q", os.Getenv("ANOTHER_GOOD"))
	}
	_ = os.Unsetenv("GOOD")
	_ = os.Unsetenv("ANOTHER_GOOD")
}
