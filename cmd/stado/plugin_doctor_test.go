package main

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

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
