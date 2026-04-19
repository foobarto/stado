package mcp

import (
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/sandbox"
)

// ParseCapabilities converts a config.MCPServer.Capabilities slice into
// a sandbox.Policy. The string forms mirror DESIGN §"Phase 8.1":
//
//	fs:read:<path>   — read-only bind
//	fs:write:<path>  — read-write bind
//	net:deny         — no egress (unshare-net)
//	net:allow        — share host network (unrestricted)
//	net:<host>       — allow egress to one host (via stado's HTTP proxy)
//	exec:<binary>    — add binary to the exec allow-list
//	env:<VAR>        — pass through the env var
//
// Unknown forms return an error so typos don't silently widen the
// sandbox. Empty slice returns a zero Policy (no fs/exec/env
// entries, default Net kind).
func ParseCapabilities(caps []string) (sandbox.Policy, error) {
	var p sandbox.Policy
	netHosts := []string{}
	netSet := false

	for _, raw := range caps {
		parts := strings.SplitN(raw, ":", 3)
		if len(parts) < 2 {
			return sandbox.Policy{}, fmt.Errorf("capability %q: missing ':<value>'", raw)
		}
		kind := parts[0]
		rest := strings.Join(parts[1:], ":")

		switch kind {
		case "fs":
			sub := strings.SplitN(rest, ":", 2)
			if len(sub) != 2 {
				return sandbox.Policy{}, fmt.Errorf("capability %q: fs requires :read:<path> or :write:<path>", raw)
			}
			switch sub[0] {
			case "read":
				p.FSRead = append(p.FSRead, sub[1])
			case "write":
				p.FSWrite = append(p.FSWrite, sub[1])
			default:
				return sandbox.Policy{}, fmt.Errorf("capability %q: fs mode must be read|write, got %q", raw, sub[0])
			}
		case "net":
			switch rest {
			case "deny":
				p.Net.Kind = sandbox.NetDenyAll
			case "allow":
				p.Net.Kind = sandbox.NetAllowAll
			default:
				p.Net.Kind = sandbox.NetAllowHosts
				netHosts = append(netHosts, rest)
			}
			netSet = true
		case "exec":
			p.Exec = append(p.Exec, rest)
		case "env":
			p.Env = append(p.Env, rest)
		default:
			return sandbox.Policy{}, fmt.Errorf("capability %q: unknown kind %q (want fs|net|exec|env)", raw, kind)
		}
	}
	if netSet {
		p.Net.Hosts = netHosts
	}
	return p, nil
}
