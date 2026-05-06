package main

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// TestCrossCheckSandbox_OffMode: sandbox.mode = "off" yields no findings.
func TestCrossCheckSandbox_OffMode(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"net:http_request", "fs:read:/etc/passwd"}}
	got := crossCheckSandbox(mf, config.Sandbox{Mode: "off"})
	if len(got) != 0 {
		t.Errorf("off mode should produce no findings; got %v", got)
	}
}

// TestCrossCheckSandbox_NetBlocked: sandbox.wrap.network = "off"
// flags every net:* cap as a hard error.
func TestCrossCheckSandbox_NetBlocked(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"net:http_request", "net:dial:tcp"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode: "wrap",
		Wrap: config.SandboxWrap{Network: "off"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 findings; got %d (%v)", len(got), got)
	}
	for _, f := range got {
		if f.Severity != "error" {
			t.Errorf("net cap with network=off should be error; got %v", f)
		}
		if !strings.Contains(f.Note, "network = \"off\"") {
			t.Errorf("note should mention network=off; got %q", f.Note)
		}
	}
}

// TestCrossCheckSandbox_NetNamespacedNoProxy: namespaced netns
// without http_proxy is a hard error for net:* caps.
func TestCrossCheckSandbox_NetNamespacedNoProxy(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"net:http_request"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode: "wrap",
		Wrap: config.SandboxWrap{Network: "namespaced"},
	})
	if len(got) != 1 || got[0].Severity != "error" {
		t.Errorf("namespaced no-proxy should be error; got %v", got)
	}
}

// TestCrossCheckSandbox_NetNamespacedWithProxy: namespaced netns
// + http_proxy set is informational (route via proxy).
func TestCrossCheckSandbox_NetNamespacedWithProxy(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"net:http_request"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode:      "wrap",
		HTTPProxy: "http://127.0.0.1:8080",
		Wrap:      config.SandboxWrap{Network: "namespaced"},
	})
	if len(got) != 1 || got[0].Severity != "info" {
		t.Errorf("namespaced + proxy should be info; got %v", got)
	}
}

// TestCrossCheckSandbox_FsAbsoluteNotBound: an absolute fs:read
// path NOT in [sandbox.wrap].bind_ro is a warning.
func TestCrossCheckSandbox_FsAbsoluteNotBound(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"fs:read:/etc/passwd"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode: "wrap",
		Wrap: config.SandboxWrap{BindRO: []string{"/usr"}},
	})
	if len(got) != 1 || got[0].Severity != "warn" {
		t.Errorf("unbound /etc/passwd should be warn; got %v", got)
	}
}

// TestCrossCheckSandbox_FsAbsoluteBound: an absolute fs:read path
// inside a bound directory is silent (no finding).
func TestCrossCheckSandbox_FsAbsoluteBound(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"fs:read:/usr/bin/grep"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode: "wrap",
		Wrap: config.SandboxWrap{BindRO: []string{"/usr"}},
	})
	for _, f := range got {
		if strings.HasPrefix(f.Cap, "fs:") {
			t.Errorf("bound /usr/bin/grep should produce no fs finding; got %v", f)
		}
	}
}

// TestCrossCheckSandbox_FsWorkdirRooted: workdir-rooted fs caps
// (".", "./...", relative paths) are silent — stado auto-binds.
func TestCrossCheckSandbox_FsWorkdirRooted(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"fs:read:.", "fs:write:./output"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode: "wrap",
	})
	for _, f := range got {
		if strings.HasPrefix(f.Cap, "fs:") {
			t.Errorf("workdir-rooted fs cap should be silent; got %v", f)
		}
	}
}

// TestCrossCheckSandbox_FsWriteRequiresBindRW: an absolute fs:write
// path must be in bind_rw, not just bind_ro.
func TestCrossCheckSandbox_FsWriteRequiresBindRW(t *testing.T) {
	mf := &plugins.Manifest{Capabilities: []string{"fs:write:/var/log/app"}}
	got := crossCheckSandbox(mf, config.Sandbox{
		Mode: "wrap",
		Wrap: config.SandboxWrap{BindRO: []string{"/var/log"}}, // RO won't satisfy WRITE
	})
	if len(got) != 1 || got[0].Severity != "warn" {
		t.Errorf("write to RO-only bind should warn; got %v", got)
	}
	if !strings.Contains(got[0].Note, "bind_rw") {
		t.Errorf("note should mention bind_rw; got %q", got[0].Note)
	}
}

// TestPathInBindList: subpaths of bound dirs match; unrelated paths don't.
func TestPathInBindList(t *testing.T) {
	binds := []string{"/usr", "/var/log/"}
	cases := map[string]bool{
		"/usr":          true,
		"/usr/bin/grep": true,
		"/usr/local":    true,
		"/etc":          false,
		"/var/log":      true,
		"/var/log/app":  true,
		"/var":          false, // not under /var/log
	}
	for path, want := range cases {
		if got := pathInBindList(path, binds); got != want {
			t.Errorf("pathInBindList(%q, ...) = %v, want %v", path, got, want)
		}
	}
}

// TestClassifyCapability checks the manifest-cap → required-surface
// mapping that the doctor command uses to render its compatibility
// table. Each case includes a brief justification so the table is
// self-documenting.
func TestClassifyCapability(t *testing.T) {
	cases := []struct {
		cap       string
		want      pluginRequirement
		descMatch string // substring expected in the human-readable note
	}{
		{"fs:read:.", requireWorkdir, "workdir-rooted"},
		{"fs:read:./notes", requireWorkdir, "workdir-rooted"},
		{"fs:write:.", requireWorkdir, "workdir-rooted"},
		{"fs:read:/abs/path", requireNothing, "absolute path"},
		{"fs:write:/abs/path", requireNothing, "absolute path"},
		{"net:http_get", requireToolHost, "bundled-tool import"},
		{"net:example.com", requireToolHost, "bundled-tool import"},
		{"exec:bash", requireFullAgentLoop, "sandbox.Runner"},
		{"exec:shallow_bash", requireFullAgentLoop, "sandbox.Runner"},
		{"exec:search", requireToolHost, "bundled-tool import"},
		{"exec:ast_grep", requireToolHost, "bundled-tool import"},
		{"lsp:query", requireToolHost, "bundled-tool import (LSP)"},
		{"session:read", requireSession, "session-aware"},
		{"session:fork", requireSession, "session-aware"},
		{"llm:invoke", requireSession, "session-aware"},
		{"llm:invoke:50000", requireSession, "session-aware"},
		{"memory:propose", requireSession, "session-aware"},
		{"memory:read", requireSession, "session-aware"},
		{"memory:write", requireSession, "session-aware"},
		{"ui:approval", requireUIApproval, "approval bridge"},
	}
	for _, c := range cases {
		got := classifyCapability(c.cap)
		if got.requirement != c.want {
			t.Errorf("classifyCapability(%q).requirement = %v, want %v",
				c.cap, got.requirement, c.want)
		}
		if !strings.Contains(got.note, c.descMatch) {
			t.Errorf("classifyCapability(%q).note = %q, want contains %q",
				c.cap, got.note, c.descMatch)
		}
	}
}

// TestBuildPluginDoctorReport_ClearOutput exercises the rendering for
// a plugin with mixed capabilities (the realistic htb-style case)
// and asserts the operator gets actionable signal.
func TestBuildPluginDoctorReport_ClearOutput(t *testing.T) {
	mf := &plugins.Manifest{
		Name:            "demo",
		Version:         "1.2.3",
		Author:          "Test Author",
		AuthorPubkeyFpr: "abcdef0123456789",
		WASMSHA256:      strings.Repeat("a", 64),
		MinStadoVersion: "0.1.0",
		Tools: []plugins.ToolDef{
			{Name: "fetch", Description: "Fetch and cache a URL"},
		},
		Capabilities: []string{"net:http_get", "fs:write:/var/cache/x"},
	}
	report, err := buildPluginDoctorReport(mf, t.TempDir())
	if err != nil {
		t.Fatalf("buildPluginDoctorReport: %v", err)
	}
	for _, want := range []string{
		"Plugin:    demo v1.2.3",
		"Author:    Test Author",
		"Signer:    abcdef0123456789",
		"WASM:      sha256:aaaaaaaaaaaa",
		"fetch",
		"Fetch and cache a URL",
		"net:http_get",
		"bundled-tool import (stado_http_get)",
		"fs:write:/var/cache/x",
		"absolute path",
		"stado plugin run --with-tool-host demo-1.2.3 fetch",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("doctor report missing %q:\n%s", want, report)
		}
	}
}

// TestBuildPluginDoctorReport_ExecBashMarkedFullLoopOnly verifies
// the EP-0028 "exec:bash refused under --with-tool-host" callout.
func TestBuildPluginDoctorReport_ExecBashMarkedFullLoopOnly(t *testing.T) {
	mf := &plugins.Manifest{
		Name:         "needs-bash",
		Version:      "0.1.0",
		Author:       "Test",
		Tools:        []plugins.ToolDef{{Name: "shell"}},
		Capabilities: []string{"exec:bash"},
	}
	report, err := buildPluginDoctorReport(mf, t.TempDir())
	if err != nil {
		t.Fatalf("buildPluginDoctorReport: %v", err)
	}
	// Every plugin run row must be ✗ for exec:bash plugins.
	for _, line := range []string{
		"✗ stado plugin run     ",
		"✗ stado plugin run --workdir=$PWD",
		"✗ stado plugin run --with-tool-host",
	} {
		if !strings.Contains(report, line) {
			t.Errorf("expected refusal row %q in:\n%s", line, report)
		}
	}
	if !strings.Contains(report, "Use the TUI") {
		t.Errorf("expected `Use the TUI` recommendation, got:\n%s", report)
	}
}
