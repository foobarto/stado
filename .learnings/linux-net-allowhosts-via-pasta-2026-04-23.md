# Linux `net:<host>` needs a netns wrapper, not nftables inside bwrap

The `bwrap` child in stado's Linux runner has no effective capabilities
inside its user/network namespace, so trying to bolt `iptables`/`nft`
rules onto that process fails even when it runs as uid 0 in the
namespace. That makes the "proxy env vars plus shared host netns"
approach fundamentally soft.

Working fix pattern:

- keep the host-side CONNECT allowlist proxy
- wrap the `bwrap` launch in `pasta --splice-only`
- forward only the proxy port into the private namespace
- keep the proxy env vars pointed at `127.0.0.1:<proxy-port>` inside the
  sandbox so ordinary proxy-aware clients still work

This gives a real kernel-visible boundary for Linux host-allowlist
subprocesses without needing CAP_NET_ADMIN.
