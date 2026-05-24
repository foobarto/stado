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
	out := make([]string, 0, len(Safelist)+len(extra))
	for _, key := range Safelist {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	out = append(out, extra...)
	return out
}
