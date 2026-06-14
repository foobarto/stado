package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadACPProvider_RegisterMCP guards EP-0032: the register_mcp consent flag
// must parse into ACPProvider.RegisterMCP. Before the fix the field (and koanf
// tag) didn't exist, so register_mcp in config.toml was silently ignored and
// stado's MCP auto-registration was unreachable.
func TestLoadACPProvider_RegisterMCP(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[acp.providers.gemini-acp]\n" +
		"binary = \"gemini\"\n" +
		"args = [\"--acp\"]\n" +
		"register_mcp = true\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	p, ok := cfg.ACP.Providers["gemini-acp"]
	if !ok {
		t.Fatal("acp provider gemini-acp not loaded")
	}
	if !p.RegisterMCP {
		t.Error("register_mcp = true should parse into ACPProvider.RegisterMCP (EP-0032)")
	}
}
