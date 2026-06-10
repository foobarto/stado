package config

import "strings"

// redactedValue is the placeholder shown in place of a secret. The
// angle-bracket form is unambiguous in JSON and survives shell pipelines.
const redactedValue = "<redacted>"

// Redacted returns a copy of the config with secret-bearing VALUES replaced
// by "<redacted>", leaving the structure and every non-secret field intact.
// Use it before any operator-visible serialisation (e.g. `config show`,
// `config show --json`) so credentials stored in config.toml are never
// printed. (R7)
//
// Redacted fields: OTel.Headers values (OTLP bearer tokens), MCP server Env
// values, MCP/ACP wrapped-provider Env (KEY=value) values, and any
// credentials embedded in Sandbox.HTTPProxy. Fields that store only env-var
// NAMES (APIKeyEnv, InheritEnv) are not secret and are left as-is.
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	cp := *c // shallow copy; every field redacted below gets a fresh map/slice
	// so the caller's original config is never mutated.

	if len(c.OTel.Headers) > 0 {
		h := make(map[string]string, len(c.OTel.Headers))
		for k := range c.OTel.Headers {
			h[k] = redactedValue
		}
		cp.OTel.Headers = h
	}

	if len(c.MCP.Servers) > 0 {
		servers := make(map[string]MCPServer, len(c.MCP.Servers))
		for name, s := range c.MCP.Servers {
			if len(s.Env) > 0 {
				env := make(map[string]string, len(s.Env))
				for k := range s.Env {
					env[k] = redactedValue
				}
				s.Env = env
			}
			servers[name] = s
		}
		cp.MCP.Servers = servers
	}

	if len(c.MCP.Providers) > 0 {
		providers := make(map[string]MCPProviderWrapped, len(c.MCP.Providers))
		for name, p := range c.MCP.Providers {
			p.Env = redactEnvSlice(p.Env)
			providers[name] = p
		}
		cp.MCP.Providers = providers
	}

	if len(c.ACP.Providers) > 0 {
		providers := make(map[string]ACPProvider, len(c.ACP.Providers))
		for name, p := range c.ACP.Providers {
			p.Env = redactEnvSlice(p.Env)
			providers[name] = p
		}
		cp.ACP.Providers = providers
	}

	cp.Sandbox.HTTPProxy = redactProxyCredentials(c.Sandbox.HTTPProxy)

	return &cp
}

// redactEnvSlice replaces the value half of every "KEY=value" entry with
// "<redacted>", preserving the key. Name-only entries (no "=") are kept.
func redactEnvSlice(entries []string) []string {
	if len(entries) == 0 {
		return entries
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		if k, _, found := strings.Cut(e, "="); found {
			out[i] = k + "=" + redactedValue
		} else {
			out[i] = e
		}
	}
	return out
}

// redactProxyCredentials strips an embedded "user:pass@" from a proxy URL,
// leaving the host reachable but the credentials hidden:
// http://user:pass@host:3128 → http://<redacted>@host:3128.
func redactProxyCredentials(proxy string) string {
	if !strings.Contains(proxy, "@") {
		return proxy
	}
	scheme := ""
	rest := proxy
	if i := strings.Index(proxy, "://"); i >= 0 {
		scheme = proxy[:i+3]
		rest = proxy[i+3:]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return proxy
	}
	return scheme + redactedValue + rest[at:]
}
