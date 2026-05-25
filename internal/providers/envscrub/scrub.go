// Package envscrub centralises the subprocess-env safelist used when
// stado launches an external agent / tool / MCP server. The host
// process holds the operator's cloud credentials / API keys / minisign
// secrets — passing the full inherited environment to a subprocess
// would leak those across the trust boundary every time.
//
// Originally lived in internal/providers/mcpwrap; extracted here so
// internal/providers/acpwrap can share it. Codex C3/M P1 — pre-fix
// the ACP wrapper inherited the full `os.Environ()` while MCP scrubbed
// (same trust-boundary shape, different code paths).
package envscrub

import "os"

// Safelist is the set of environment variables forwarded to an
// external subprocess by default. The operator can extend this with
// explicit Config.Env entries (which get appended after the safelisted
// values so they can override). Anything not on the list is dropped.
//
// Membership rule of thumb: variables the subprocess plausibly needs
// to locate config / a shell / a terminal stay. Credentials and
// per-host secrets — anything that names a service, account,
// session, or token — drops.
var Safelist = []string{
	"HOME",
	"PATH",
	"USER",
	"LOGNAME",
	"SHELL",
	"TERM",
	"LANG",
	"LC_ALL",
	"TMPDIR",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_CACHE_HOME",
}

// Scrub returns the subprocess environment: every safelisted entry
// from the current process's environment, with extra appended after
// so explicit per-config overrides win on duplicate keys.
//
// Replaces the local copies that lived in mcpwrap/provider.go and
// acpwrap/mcpmount.go. PR #048 introduced the MCP-side scrub; this
// package generalises it so every wrapped-agent surface scrubs
// uniformly. Codex C3/M P1.
func Scrub(extra []string) []string {
	return ScrubWithInherits(extra, nil)
}

// ScrubWithInherits is Scrub plus an operator-supplied list of
// additional env-var NAMES to extract from the current process's
// environment and forward to the subprocess. Used by wrapped-agent
// providers that need credential pass-through per EP-0032's
// "operator's job to manage env" trust model — e.g. `gemini-acp`
// inherits `GEMINI_API_KEY`, `opencode-acp` inherits its OAuth
// tokens, `codex-mcp` inherits whatever its operator configured.
//
// Order in the returned slice: safelisted first, then extras
// (per-config explicit `KEY=VALUE` entries), then inheritKeys-
// extracted entries. `extras` win on duplicate keys against the
// safelist because they're appended later; inheritKeys-extracted
// entries do NOT override `extras` (operator-explicit value wins
// over inherited). Decision recorded at
// .agent/decisions/2026-05-25-acpwrap-inherit-env-opt-in.md.
func ScrubWithInherits(extra []string, inheritKeys []string) []string {
	out := make([]string, 0, len(Safelist)+len(extra)+len(inheritKeys))
	for _, key := range Safelist {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	out = append(out, extra...)
	// Build a quick-lookup of keys already present in `out` so an
	// inheritKeys entry doesn't shadow an explicit Config.Env value
	// that the operator set with a specific value (e.g. a per-
	// provider GEMINI_API_KEY=test-key for sandboxing).
	present := map[string]bool{}
	for _, kv := range out {
		if name, _, ok := splitKV(kv); ok {
			present[name] = true
		}
	}
	for _, key := range inheritKeys {
		if key == "" || present[key] {
			continue
		}
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
			present[key] = true
		}
	}
	return out
}

// splitKV splits "KEY=VALUE" into ("KEY", "VALUE", true). Returns
// ok=false when the entry lacks an `=` or starts with `=`.
func splitKV(kv string) (string, string, bool) {
	for i, c := range kv {
		if c == '=' {
			if i == 0 {
				return "", "", false
			}
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}
