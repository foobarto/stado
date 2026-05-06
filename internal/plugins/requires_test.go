package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequire(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantMin  string
		wantErr  bool
	}{
		{"http-session", "http-session", "", false},
		{"http-session >= 0.1.0", "http-session", "0.1.0", false},
		{"http-session >= v0.1.0", "http-session", "0.1.0", false},
		{"  http-session  >=  0.2.0  ", "http-session", "0.2.0", false},
		{"http-session > 0.1.0", "", "", true}, // unsupported operator
		{"", "", "", true},
		{"foo bar baz qux", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRequire(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseRequire(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr {
				if got.Name != tc.wantName || got.MinVersion != tc.wantMin {
					t.Errorf("ParseRequire(%q) = %+v, want {Name:%q MinVersion:%q}", tc.in, got, tc.wantName, tc.wantMin)
				}
			}
		})
	}
}

func TestCheckRequires_AllSatisfied(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "http-session-0.1.0"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "secrets-store-0.2.0"), 0o755)

	m := &Manifest{Requires: []string{
		"http-session >= 0.1.0",
		"secrets-store",
	}}
	if err := CheckRequires(m, dir); err != nil {
		t.Errorf("CheckRequires should succeed; got: %v", err)
	}
}

func TestCheckRequires_MissingPlugin(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Requires: []string{"http-session >= 0.1.0"}}
	err := CheckRequires(m, dir)
	if err == nil {
		t.Fatal("expected error for missing plugin")
	}
	if !strings.Contains(err.Error(), "http-session") {
		t.Errorf("error should mention http-session; got: %v", err)
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error should mention `not installed`; got: %v", err)
	}
}

func TestCheckRequires_VersionTooLow(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "http-session-0.0.5"), 0o755)
	m := &Manifest{Requires: []string{"http-session >= 0.1.0"}}
	err := CheckRequires(m, dir)
	if err == nil {
		t.Fatal("expected error for version-too-low")
	}
	if !strings.Contains(err.Error(), "0.0.5") || !strings.Contains(err.Error(), "0.1.0") {
		t.Errorf("error should mention installed AND required versions; got: %v", err)
	}
}

func TestCheckRequires_HighestVersionWins(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plug-0.1.0", "plug-0.2.0", "plug-0.1.5"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}
	// Required 0.2.0; the dir has 0.1.0, 0.1.5, AND 0.2.0 — should
	// pick 0.2.0 and pass.
	m := &Manifest{Requires: []string{"plug >= 0.2.0"}}
	if err := CheckRequires(m, dir); err != nil {
		t.Errorf("highest version should satisfy; got: %v", err)
	}
}

func TestCheckRequires_MultipleErrors(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Requires: []string{
		"missing-a >= 0.1.0",
		"missing-b",
		"missing-c >= 1.0.0",
	}}
	err := CheckRequires(m, dir)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"missing-a", "missing-b", "missing-c"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should mention %q; got: %v", name, err)
		}
	}
}

func TestCheckRequires_EmptyManifest(t *testing.T) {
	if err := CheckRequires(&Manifest{}, t.TempDir()); err != nil {
		t.Errorf("empty Requires should be a no-op; got: %v", err)
	}
	if err := CheckRequires(nil, t.TempDir()); err != nil {
		t.Errorf("nil manifest should be a no-op; got: %v", err)
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		actual   string
		required string
		want     bool
	}{
		{"0.1.0", "0.1.0", true},
		{"0.2.0", "0.1.0", true},
		{"0.1.0", "0.2.0", false},
		{"v0.2.0", "0.1.0", true}, // v-prefix tolerated
		{"0.1.5", "0.1.4", true},
		{"1.0.0", "0.99.99", true},
	}
	for _, tc := range cases {
		t.Run(tc.actual+">="+tc.required, func(t *testing.T) {
			if got := versionAtLeast(tc.actual, tc.required); got != tc.want {
				t.Errorf("versionAtLeast(%q, %q) = %v, want %v", tc.actual, tc.required, got, tc.want)
			}
		})
	}
}
