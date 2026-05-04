package acpwrap

import (
	"context"
	"os"
	"testing"
)

func TestLookupAgentCheck_KnownAgents(t *testing.T) {
	cases := []struct {
		binary     string
		wantName   string
		wantHonors bool
	}{
		// Honors=true cases — no setup needed.
		{"opencode", "opencode", true},
		{"/usr/local/bin/opencode", "opencode", true},
		{"zed", "zed", true},
		{"hermes", "hermes", true},
		{"/opt/hermes/venv/bin/hermes", "hermes", true},
		// Honors=false cases — registration required.
		{"gemini", "gemini", false},
		{"/opt/node/bin/gemini", "gemini", false},
		{"claude", "claude", false},
		{"codex", "codex", false},
		// Case-insensitive normalisation.
		{"Gemini", "gemini", false},
	}
	for _, tc := range cases {
		t.Run(tc.binary, func(t *testing.T) {
			c, ok := lookupAgentCheck(tc.binary)
			if !ok {
				t.Fatalf("expected %q to match a known agent", tc.binary)
			}
			if c.Name != tc.wantName {
				t.Errorf("name = %q, want %q", c.Name, tc.wantName)
			}
			if c.Honors != tc.wantHonors {
				t.Errorf("honors = %v, want %v", c.Honors, tc.wantHonors)
			}
		})
	}
}

func TestLookupAgentCheck_Unknown(t *testing.T) {
	if _, ok := lookupAgentCheck("not-a-real-agent-xyz"); ok {
		t.Error("expected unknown agent to miss the lookup")
	}
	if _, ok := lookupAgentCheck(""); ok {
		t.Error("expected empty binary to miss the lookup")
	}
}

func TestFormatRegisterDescription_SubstitutesStadoBin(t *testing.T) {
	check := agentChecksByBinary["gemini"]
	got := formatRegisterDescription(check, "/abs/path/to/stado")
	want := "gemini mcp add -s user stado /abs/path/to/stado mcp-server"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestFormatRegisterDescription_CodexUsesDoubleDashSeparator(t *testing.T) {
	// codex's `mcp add` requires a `--` separator between the name
	// and the command. Verify the template preserves it.
	check := agentChecksByBinary["codex"]
	got := formatRegisterDescription(check, "/abs/path/to/stado")
	want := "codex mcp add stado -- /abs/path/to/stado mcp-server"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestSubstituteStadoBin_ReplacesAllOccurrences(t *testing.T) {
	args := []string{"mcp", "add", "stado", "{STADO_BIN}", "mcp-server"}
	got := substituteStadoBin(args, "/abs/x")
	want := []string{"mcp", "add", "stado", "/abs/x", "mcp-server"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: %v != %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSubstituteStadoBin_PreservesUserScopeFlag(t *testing.T) {
	// gemini and claude both need -s user; ensure substitution
	// doesn't disturb the flag arrangement.
	check := agentChecksByBinary["gemini"]
	got := substituteStadoBin(check.RegisterArgs, "/x")
	wantFirst := []string{"mcp", "add", "-s", "user", "stado"}
	for i, w := range wantFirst {
		if got[i] != w {
			t.Errorf("[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestJSONMcpServersHasStado(t *testing.T) {
	cases := []struct {
		name   string
		parsed any
		want   bool
	}{
		{"present", map[string]any{"mcpServers": map[string]any{"stado": map[string]any{}}}, true},
		{"present alongside other servers", map[string]any{"mcpServers": map[string]any{"other": map[string]any{}, "stado": map[string]any{"command": "/x"}}}, true},
		{"absent", map[string]any{"mcpServers": map[string]any{"other": map[string]any{}}}, false},
		{"empty mcpServers", map[string]any{"mcpServers": map[string]any{}}, false},
		{"no mcpServers key", map[string]any{"otherKey": "value"}, false},
		{"non-map root", "garbage", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonMcpServersHasStado(tc.parsed); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTomlMcpServersHasStado(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"unquoted section header", "[mcp_servers.stado]\ncommand = \"/x\"\n", true},
		{"quoted section header", "[mcp_servers.\"stado\"]\ncommand = \"/x\"\n", true},
		{"section among others", "[mcp_servers.other]\nx = 1\n[mcp_servers.stado]\ny = 2\n", true},
		{"missing section", "[mcp_servers.other]\nx = 1\n", false},
		{"empty file", "", false},
		{"nearby but not matching", "[mcp_servers.stado2]\nx = 1\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tomlMcpServersHasStado([]byte(tc.raw)); got != tc.want {
				t.Errorf("got %v, want %v for %q", got, tc.want, tc.raw)
			}
		})
	}
}

func TestIsStadoRegisteredInConfig_GeminiFixture(t *testing.T) {
	// Build a temp gemini-shaped config with stado registered;
	// verify the parser recognises it.
	dir := t.TempDir()
	cfgPath := dir + "/settings.json"
	if err := os.WriteFile(cfgPath, []byte(`{"mcpServers":{"stado":{"command":"/x","args":["mcp-server"]}}}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	check := agentMCPCheck{
		ConfigPath:         cfgPath,
		ConfigFormat:       "json",
		ConfigStadoPresent: jsonMcpServersHasStado,
	}
	got, err := isStadoRegisteredInConfig(check)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got {
		t.Error("expected stado registered, got false")
	}
}

func TestIsStadoRegisteredInConfig_MissingFile_ReturnsFalseNoError(t *testing.T) {
	check := agentMCPCheck{
		ConfigPath:         t.TempDir() + "/does-not-exist.json",
		ConfigFormat:       "json",
		ConfigStadoPresent: jsonMcpServersHasStado,
	}
	got, err := isStadoRegisteredInConfig(check)
	if err != nil {
		t.Errorf("missing file should not error, got: %v", err)
	}
	if got {
		t.Error("missing file should report not-registered")
	}
}

func TestCheckMCPRegistration_HonorsTrue_NoOp(t *testing.T) {
	// Capture stderr to verify nothing was emitted.
	r, w, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = saved; _ = w.Close(); _ = r.Close() })

	CheckMCPRegistration(context.Background(), "/path/to/opencode", "/path/to/stado")
	_ = w.Close()
	os.Stderr = saved

	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	if n > 0 {
		t.Errorf("expected no stderr output for honors=true agent, got: %s", buf[:n])
	}
}

func TestCheckMCPRegistration_UnknownAgent_NoOp(t *testing.T) {
	// Unknown binaries should produce no warnings — we have nothing
	// useful to say, the existing wire-level fallback handles it.
	r, w, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = saved; _ = w.Close(); _ = r.Close() })

	CheckMCPRegistration(context.Background(), "/path/to/totally-unknown-cli", "/path/to/stado")
	_ = w.Close()
	os.Stderr = saved

	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	if n > 0 {
		t.Errorf("expected no stderr output for unknown agent, got: %s", buf[:n])
	}
}
