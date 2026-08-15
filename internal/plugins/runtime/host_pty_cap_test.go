package runtime

import (
	"context"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// Security regression: the PTY spawn path (stado_pty_create) skipped the
// glob-enforcement layer that exec:proc has, so exec:pty alone (or a narrow
// exec:pty:<glob>) could run ANY binary — and opts.Cmd expands to "/bin/sh -c".
// ptyAllowed + ExecPTYGlobs (parsed from exec:pty:<glob>)
// close that; registerPTYCreate now calls ptyAllowed on the effective binary.

func TestPtyAllowed_GlobEnforcement(t *testing.T) {
	cases := []struct {
		name  string
		host  *Host
		bin   string
		allow bool
	}{
		{"no exec:pty -> deny", &Host{ExecPTY: false}, "git", false},
		{"broad exec:pty -> any binary", &Host{ExecPTY: true}, "curl", true},
		{"scoped basename match", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, "git", true},
		{"scoped basename miss", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, "curl", false},
		{"scoped denies /bin/sh fallback", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, "/bin/sh", false},
		{"abs glob match", &Host{ExecPTY: true, ExecPTYGlobs: []string{"/usr/bin/git"}}, "/usr/bin/git", true},
		{"abs glob miss", &Host{ExecPTY: true, ExecPTYGlobs: []string{"/usr/bin/git"}}, "/usr/bin/curl", false},
		// Codex #44: a basename cap must NOT authorize a path-containing
		// argv[0] whose basename happens to match — that runs an attacker
		// binary while looking like the scoped one.
		{"basename cap denies abs-path same-basename", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, "/tmp/evil/git", false},
		{"basename cap denies rel-path same-basename", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, "./git", false},
		{"basename cap denies subdir same-basename", &Host{ExecPTY: true, ExecPTYGlobs: []string{"python"}}, "subdir/python", false},
		// Codex #208/P1: backslash-separated paths (Windows form) must also be
		// treated as path-containing, not bare names.
		{"basename cap denies backslash path", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, `C:\tmp\git`, false},
		{"basename cap denies backslash rel path", &Host{ExecPTY: true, ExecPTYGlobs: []string{"git"}}, `evil\git`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.host.ptyAllowed(tc.bin); got != tc.allow {
				t.Errorf("ptyAllowed(%q) = %v; want %v", tc.bin, got, tc.allow)
			}
		})
	}
}

// TestExecPTYGlobParsing: exec:pty:<glob> populates ExecPTYGlobs (and the broad
// form leaves it empty = broad); a mixed relative-path glob is rejected
// (silent-deny footgun guard, same as exec:proc). Removed terminal:open forms
// must not grant PTY authority.
func TestExecPTYGlobParsing(t *testing.T) {
	cases := []struct {
		cap       string
		wantPTY   bool
		wantGlobs []string
	}{
		{"exec:pty", true, nil},
		{"terminal:open", false, nil},
		{"exec:pty:git", true, []string{"git"}},
		{"terminal:open:git", false, nil},
		{"exec:pty:/usr/bin/git", true, []string{"/usr/bin/git"}},
		// mixed relative-path glob -> rejected -> fail-closed deny sentinel
		// (NOT empty, which would mean broad).
		{"exec:pty:bin/git", true, []string{execGlobDeny}},
	}
	for _, tc := range cases {
		t.Run(tc.cap, func(t *testing.T) {
			h := NewHost(plugins.Manifest{Name: "p", Capabilities: []string{tc.cap}}, t.TempDir(), nil)
			if h.ExecPTY != tc.wantPTY {
				t.Errorf("ExecPTY = %v; want %v", h.ExecPTY, tc.wantPTY)
			}
			if len(h.ExecPTYGlobs) != len(tc.wantGlobs) {
				t.Fatalf("ExecPTYGlobs = %v; want %v", h.ExecPTYGlobs, tc.wantGlobs)
			}
			for i, g := range tc.wantGlobs {
				if h.ExecPTYGlobs[i] != g {
					t.Errorf("ExecPTYGlobs[%d] = %q; want %q", i, h.ExecPTYGlobs[i], g)
				}
			}
		})
	}
}

func TestRemovedTerminalImportsAreNotExported(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	h := NewHost(plugins.Manifest{Name: "p", Capabilities: []string{"exec:pty"}}, t.TempDir(), nil)
	if err := InstallHostImports(ctx, rt, h); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	exports := rt.rt.Module(NamespaceStado).ExportedFunctionDefinitions()
	for _, name := range []string{
		"stado_pty_create", "stado_pty_list", "stado_pty_write",
		"stado_pty_read", "stado_pty_signal", "stado_pty_resize",
		"stado_pty_destroy", "stado_pty_snapshot", "stado_pty_expect",
	} {
		if _, ok := exports[name]; !ok {
			t.Errorf("canonical import %s is not exported", name)
		}
	}
	for _, name := range []string{
		"stado_terminal_open", "stado_terminal_list", "stado_terminal_write",
		"stado_terminal_read", "stado_terminal_signal", "stado_terminal_resize",
		"stado_terminal_close", "stado_terminal_snapshot", "stado_terminal_expect",
	} {
		if _, ok := exports[name]; ok {
			t.Errorf("removed compatibility import %s is still exported", name)
		}
	}
}

// TestExecCap_MalformedGlobFailsClosed (Codex P1 on #196): a scoped cap whose
// ONLY glob is malformed must DENY, not fall back to broad. Covers both pty and
// proc since they share appendExecGlob + execGlobMatch.
func TestExecCap_MalformedGlobFailsClosed(t *testing.T) {
	pty := NewHost(plugins.Manifest{Name: "p", Capabilities: []string{"exec:pty:bin/git"}}, t.TempDir(), nil)
	if !pty.ExecPTY {
		t.Fatal("ExecPTY should be set")
	}
	if pty.ptyAllowed("git") || pty.ptyAllowed("/bin/sh") || pty.ptyAllowed("curl") {
		t.Error("a malformed-only exec:pty glob must deny every binary (fail closed), not allow broad")
	}
	proc := NewHost(plugins.Manifest{Name: "p", Capabilities: []string{"exec:proc:bin/bash"}}, t.TempDir(), nil)
	if !proc.ExecProc {
		t.Fatal("ExecProc should be set")
	}
	if proc.procAllowed("bash") || proc.procAllowed("/bin/sh") {
		t.Error("a malformed-only exec:proc glob must deny every binary (fail closed), not allow broad")
	}
}
