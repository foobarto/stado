package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedacted_RemovesSecretsKeepsShape(t *testing.T) {
	cfg := &Config{}
	cfg.OTel.Headers = map[string]string{"Authorization": "Bearer sk-supersecret"}
	cfg.MCP.Servers = map[string]MCPServer{"gh": {Env: map[string]string{"GITHUB_TOKEN": "ghp_secret"}}}
	cfg.MCP.Providers = map[string]MCPProviderWrapped{"codex": {Env: []string{"OPENAI_API_KEY=sk-mcp-secret"}}}
	cfg.ACP.Providers = map[string]ACPProvider{"gemini": {Env: []string{"GEMINI_API_KEY=sk-acp-secret"}}}
	cfg.Sandbox.HTTPProxy = "http://user:proxysecret@proxy.corp:3128"

	red := cfg.Redacted()

	// No secret value survives marshaling (secrets have no special chars,
	// so they'd appear verbatim if leaked).
	out := mustMarshal(t, red)
	for _, secret := range []string{"sk-supersecret", "ghp_secret", "sk-mcp-secret", "sk-acp-secret", "proxysecret"} {
		if strings.Contains(out, secret) {
			t.Errorf("redacted output leaked secret %q:\n%s", secret, out)
		}
	}

	// Shape preserved (keys/names kept) and values replaced with the
	// placeholder — checked on the struct to avoid JSON HTML-escaping noise.
	if got := red.OTel.Headers["Authorization"]; got != redactedValue {
		t.Errorf("OTel header value = %q, want %q", got, redactedValue)
	}
	if got := red.MCP.Servers["gh"].Env["GITHUB_TOKEN"]; got != redactedValue {
		t.Errorf("MCP server env value = %q, want %q", got, redactedValue)
	}
	if got := red.MCP.Providers["codex"].Env[0]; got != "OPENAI_API_KEY="+redactedValue {
		t.Errorf("MCP provider env = %q, want key=%s", got, redactedValue)
	}
	if got := red.ACP.Providers["gemini"].Env[0]; got != "GEMINI_API_KEY="+redactedValue {
		t.Errorf("ACP provider env = %q, want key=%s", got, redactedValue)
	}
	if got := red.Sandbox.HTTPProxy; got != "http://"+redactedValue+"@proxy.corp:3128" {
		t.Errorf("proxy = %q, want host preserved + creds redacted", got)
	}
}

func TestRedacted_DoesNotMutateOriginal(t *testing.T) {
	cfg := &Config{}
	cfg.OTel.Headers = map[string]string{"Authorization": "Bearer sk-orig"}
	cfg.MCP.Servers = map[string]MCPServer{"gh": {Env: map[string]string{"GITHUB_TOKEN": "ghp_orig"}}}
	cfg.MCP.Providers = map[string]MCPProviderWrapped{"codex": {Env: []string{"OPENAI_API_KEY=sk-orig"}}}

	_ = cfg.Redacted()

	if cfg.OTel.Headers["Authorization"] != "Bearer sk-orig" {
		t.Error("Redacted mutated the original OTel.Headers")
	}
	if cfg.MCP.Servers["gh"].Env["GITHUB_TOKEN"] != "ghp_orig" {
		t.Error("Redacted mutated the original MCP server Env")
	}
	if cfg.MCP.Providers["codex"].Env[0] != "OPENAI_API_KEY=sk-orig" {
		t.Error("Redacted mutated the original MCP provider Env")
	}
}

func TestRedacted_NilAndEmptySafe(t *testing.T) {
	var nilCfg *Config
	if nilCfg.Redacted() != nil {
		t.Error("nil config Redacted should return nil")
	}
	_ = (&Config{}).Redacted() // empty config must not panic
}

func TestRedactProxyCredentials(t *testing.T) {
	cases := map[string]string{
		"http://user:pass@host:3128": "http://" + redactedValue + "@host:3128",
		"http://host:3128":           "http://host:3128",
		"":                           "",
	}
	for in, want := range cases {
		if got := redactProxyCredentials(in); got != want {
			t.Errorf("redactProxyCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
