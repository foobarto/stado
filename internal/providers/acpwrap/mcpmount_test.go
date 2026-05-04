package acpwrap

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildStadoMCPMount_FieldShape(t *testing.T) {
	mount, err := BuildStadoMCPMount(nil)
	if err != nil {
		t.Fatalf("BuildStadoMCPMount: %v", err)
	}
	if mount.Name != "stado" {
		t.Errorf("Name = %q, want %q", mount.Name, "stado")
	}
	if !filepath.IsAbs(mount.Command) {
		t.Errorf("Command must be absolute (so wrapped agent doesn't need $PATH lookup): %q", mount.Command)
	}
	if len(mount.Args) != 1 || mount.Args[0] != "mcp-server" {
		t.Errorf("Args = %v, want [mcp-server]", mount.Args)
	}
}

func TestBuildStadoMCPMount_PassesThroughExtraEnv(t *testing.T) {
	extra := []MCPServerEnv{
		{Name: "STADO_TELEMETRY_OFF", Value: "1"},
		{Name: "CUSTOM_DEBUG", Value: "verbose"},
	}
	mount, err := BuildStadoMCPMount(extra)
	if err != nil {
		t.Fatalf("BuildStadoMCPMount: %v", err)
	}
	envByName := map[string]string{}
	for _, e := range mount.Env {
		envByName[e.Name] = e.Value
	}
	if envByName["STADO_TELEMETRY_OFF"] != "1" {
		t.Errorf("STADO_TELEMETRY_OFF lost: env = %+v", mount.Env)
	}
	if envByName["CUSTOM_DEBUG"] != "verbose" {
		t.Errorf("CUSTOM_DEBUG lost: env = %+v", mount.Env)
	}
}

func TestBuildStadoMCPMount_PicksUpXDGSafelist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/test-stado-config")
	t.Setenv("STADO_CONFIG_PATH", "/tmp/test-stado-config/config.toml")
	mount, err := BuildStadoMCPMount(nil)
	if err != nil {
		t.Fatalf("BuildStadoMCPMount: %v", err)
	}
	envByName := map[string]string{}
	for _, e := range mount.Env {
		envByName[e.Name] = e.Value
	}
	if envByName["XDG_CONFIG_HOME"] != "/tmp/test-stado-config" {
		t.Errorf("XDG_CONFIG_HOME not propagated: %v", envByName)
	}
	if envByName["STADO_CONFIG_PATH"] != "/tmp/test-stado-config/config.toml" {
		t.Errorf("STADO_CONFIG_PATH not propagated: %v", envByName)
	}
}

func TestBuildStadoMCPMount_DoesNotLeakUnsafelistedEnv(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "should-not-leak")
	t.Setenv("API_TOKEN", "should-not-leak")
	mount, err := BuildStadoMCPMount(nil)
	if err != nil {
		t.Fatalf("BuildStadoMCPMount: %v", err)
	}
	for _, e := range mount.Env {
		if e.Name == "AWS_SECRET_ACCESS_KEY" || e.Name == "API_TOKEN" {
			t.Errorf("env safelist leaked %s — wrapped agent may surface env to user", e.Name)
		}
	}
}

func TestBuildStadoMCPMount_SerialisesToCanonicalACPShape(t *testing.T) {
	// The wrapped agent's stdin parser expects the canonical Zed-spec
	// stdio shape: name, command, args, env (env entries with
	// {name, value}). A camelCase or different-key emission would be
	// rejected silently. Verify the JSON we'd emit matches.
	mount, err := BuildStadoMCPMount([]MCPServerEnv{{Name: "X", Value: "y"}})
	if err != nil {
		t.Fatalf("BuildStadoMCPMount: %v", err)
	}
	buf, err := json.Marshal(mount)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(buf)

	// Required fields.
	for _, field := range []string{`"name":"stado"`, `"command":`, `"args":`, `"env":`} {
		if !strings.Contains(wire, field) {
			t.Errorf("wire JSON missing %q: %s", field, wire)
		}
	}
	// Env entry shape: {"name":"X","value":"y"} — both keys lowercase.
	if !strings.Contains(wire, `"name":"X"`) || !strings.Contains(wire, `"value":"y"`) {
		t.Errorf("env entry not in canonical {name, value} shape: %s", wire)
	}
}

func TestValidateMount_RejectsEmptyName(t *testing.T) {
	if err := validateMount(MCPServerMount{Command: "/usr/bin/stado"}); err == nil {
		t.Error("expected error for empty Name")
	}
}

func TestValidateMount_RejectsRelativeCommand(t *testing.T) {
	if err := validateMount(MCPServerMount{Name: "stado", Command: "stado"}); err == nil {
		t.Error("expected error for relative command path (wrapped agent may not have $PATH)")
	}
}

func TestValidateMount_RejectsEmptyCommand(t *testing.T) {
	if err := validateMount(MCPServerMount{Name: "stado"}); err == nil {
		t.Error("expected error for empty Command")
	}
}

func TestValidateMount_RejectsEnvMissingName(t *testing.T) {
	m := MCPServerMount{
		Name:    "stado",
		Command: "/usr/local/bin/stado",
		Env:     []MCPServerEnv{{Name: "OK", Value: "yes"}, {Value: "no-name"}},
	}
	if err := validateMount(m); err == nil {
		t.Error("expected error for env entry with empty Name")
	}
}

func TestValidateMount_AcceptsValidMount(t *testing.T) {
	m := MCPServerMount{
		Name:    "stado",
		Command: "/usr/local/bin/stado",
		Args:    []string{"mcp-server"},
		Env:     []MCPServerEnv{{Name: "HOME", Value: "/home/x"}},
	}
	if err := validateMount(m); err != nil {
		t.Errorf("expected accept, got %v", err)
	}
}
