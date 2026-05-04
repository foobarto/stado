---
ep: 0031
title: Path-templated fs capabilities — `fs:read:cfg:state_dir/...`
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Direct extension of EP-0029 surfaced by the round-8 state-dir-info example.
see-also: [0005, 0006, 0029]
---

# EP-0031: Path-templated fs capabilities — `fs:read:cfg:state_dir/...`

## Problem

EP-0029 introduced `cfg:*` capabilities for read-only configuration
introspection (first concrete: `cfg:state_dir`). The first
end-to-end consumer (`plugins/examples/state-dir-info/`) validated
the host import works.

The next consumer — migrating `stado plugin doctor` from core to a
plugin — surfaces a gap: the doctor needs to read each installed
plugin's manifest at `<state-dir>/plugins/<id>/plugin.manifest.json`.
That requires an `fs:read:<state-dir>/plugins` capability. But
manifest capabilities are static strings parsed at install time,
and `<state-dir>` varies per operator (`$XDG_DATA_HOME` override,
Atomic Fedora `/var/home` symlink, etc.). The plugin author has
two unappealing options:

1. **Hardcode an absolute path** — fragile per-operator; the same
   problem htb-cve-lookup hit + worked around with v0.2.0.
2. **Take the state-dir as a runtime arg** — shifts the awkward
   bootstrap onto the operator (must invoke
   `stado plugin run --workdir=$(stado config show | jq ...)`),
   and `--workdir` only resolves one path-prefix per invocation.

Neither is friendly. Operator-tooling plugins need a way to
declare "I want to read under stado's state-dir, whatever that
resolves to" without hardcoding the path or shifting boilerplate
to the operator's invocation.

## Goals

- Let manifest capabilities reference cfg:* values inline:
  `fs:read:cfg:state_dir/plugins`,
  `fs:write:cfg:state_dir/scratch`,
  etc. The runtime resolves the `cfg:<name>` prefix against the
  same value that `stado_cfg_<name>` returns.
- Keep capability-grant discipline (EP-0005): the templated entry
  ONLY expands when the matching `cfg:<name>` capability is also
  declared. A plugin that declares `fs:read:cfg:state_dir/plugins`
  without `cfg:state_dir` reads nothing.
- Resolution is at-check time (lazy), not at-install time. The
  host caller may populate `host.StateDir` AFTER `NewHost` returns
  (the existing pattern); the path-template entry must work with
  that ordering.

## Non-goals

- Generic shell-style env-var substitution. We're not adding
  `${HOME}/...` or `$XDG_CONFIG_HOME/...` to capability paths.
  Those are operator-environment, not stado-runtime, and EP-0029
  §"Non-goals" already rejected exposing them as `cfg:*`.
- Path-template support outside `fs:*` capabilities. `net:cfg:...`
  doesn't need to exist; net caps already work with literal hosts.
  If a future capability surface needs path-templating, it can
  reuse the same `cfg:<name>[/<sub-path>]` shape with its own
  expansion call.
- Validation that the resolved path exists. `fs:read:cfg:state_dir/plugins`
  on a fresh install with no plugins yet is fine — the plugin will
  read zero matches; that's a runtime concern, not a manifest one.

## Design

### Capability syntax

```
fs:read:cfg:<name>[/<sub-path>]
fs:write:cfg:<name>[/<sub-path>]
```

Examples:

```
fs:read:cfg:state_dir                        # the state-dir itself
fs:read:cfg:state_dir/plugins                # the plugins install root
fs:write:cfg:state_dir/cache/myapp           # write under state-dir/cache
```

The leading `cfg:` discriminates this from a literal absolute path
(`/...`) and a relative-to-workdir path (`./...` or
`<bare-name>...`). On Unix, no real path begins with `cfg:`, so
the discriminator is unambiguous.

### Parser change

`NewHost`'s fs cap parser, on encountering a `cfg:`-prefixed path,
stores the entry verbatim instead of normalising it via
`normaliseCapabilityPath`. The non-`cfg:` branch is unchanged, so
existing manifests continue to work identically.

```go
case "fs":
    if len(parts) != 3 { continue }
    path := parts[2]
    var scope string
    if strings.HasPrefix(path, "cfg:") {
        scope = path           // deferred — expand at check time
    } else {
        scope = normaliseCapabilityPath(workdir, path)
    }
    switch parts[1] {
    case "read":  h.FSRead  = append(h.FSRead, scope)
    case "write": h.FSWrite = append(h.FSWrite, scope)
    }
```

### Check-time expansion

`allowRead` / `allowWrite` iterate the allow-list, expand each
entry via `expandFSEntry`, and compare. Entries that fail to expand
(cap not declared, value empty, unknown name) are silently filtered
— the plugin sees the same "no match" result as if the entry
weren't in the list.

```go
func (h *Host) expandFSEntry(raw string) string {
    if !strings.HasPrefix(raw, "cfg:") { return raw }
    rest := raw[len("cfg:"):]
    name, sub, _ := strings.Cut(rest, "/")
    var value string
    switch name {
    case "state_dir":
        if !h.CfgStateDir { return "" }
        value = h.StateDir
    default:
        return ""
    }
    if value == "" { return "" }
    if sub == "" { return value }
    return filepath.Clean(value + "/" + sub)
}
```

### Cap-pairing: must declare the matching cfg:* cap

Templated `fs:read:cfg:<name>/...` ONLY expands if the manifest
also declares `cfg:<name>`. Two reasons:

1. **Operator authorisation.** The cap declaration is the
   operator's review surface at install time. A plugin that
   accesses `<state-dir>/plugins/...` should declare it's doing
   so via the `cfg:state_dir` cap, not sneak it past via an
   undeclared template.
2. **Symmetry with the host import.** If a plugin can resolve
   `cfg:state_dir` in path templates without declaring the cap, it
   can read state-dir indirectly. The cap declaration must gate
   both surfaces equally.

This is enforced by `expandFSEntry`'s `if !h.CfgStateDir { return "" }`
short-circuit. Returns "" → no entry → no match → access denied.

### Future cfg:* names

Each new `cfg:<name>` capability that ships gets a `case <name>:`
branch in `expandFSEntry`. No other moving parts. Likely additions
once concrete consumers ask:

- `cfg:state_dir/plugins` (already covered)
- `cfg:state_dir/worktrees`
- `cfg:state_dir/memory`
- `cfg:config_dir/templates` (when `cfg:config_dir` ships)

## Migration / rollout

Pure additive. Existing manifests unchanged. The `cfg:` prefix in
fs cap paths was previously syntactically meaningless (absolute
paths start with `/`, relative paths don't have colons in their
prefix), so no existing plugin uses it.

## Failure modes

- **Plugin declares `fs:read:cfg:state_dir/plugins` but not
  `cfg:state_dir`.** Entry expands to "" → silently filtered →
  reads under `<state-dir>/plugins` are denied. The plugin sees a
  "permission denied"-style failure at runtime; the operator sees
  the missing cap if they run `plugin doctor` (which classifies
  caps and surfaces the mismatch).
- **`h.StateDir` is empty (caller didn't populate).** Same as
  above — entry filters to "". The plugin's degraded path
  applies; the operator sees a runtime "denied" without an
  obvious cap problem.
- **Unknown `cfg:<name>`.** Same fail-closed behaviour; entry
  filters to "". A future `expandFSEntry` may log a warning at
  parser time when an unknown cfg name is referenced — left out of
  this EP because the lazy at-check expansion makes a parser-time
  warning slightly off (the unknown name might be a typo or might
  be a forward-reference to a not-yet-released cfg cap on a
  newer stado).

## Test strategy

`internal/plugins/runtime/host_cfg_path_test.go` covers:

- `TestFSCap_CfgPathTemplate` — parsing, exact + subpath +
  deep-subpath matching, literal-mixed-with-templated entries.
- `TestFSCap_CfgPathTemplateRefusedWithoutCap` — fail-closed
  when the matching cfg:* cap is missing.
- `TestFSCap_CfgPathTemplateRefusedWithEmptyValue` — fail-closed
  when h.StateDir is empty.
- `TestFSCap_CfgPathTemplateUnknownName` — fail-closed for
  unknown cfg names.

End-to-end consumer test deferred to the doctor-as-plugin
migration EP.

## Open questions

- Should templated entries support multi-segment cfg refs?
  E.g., `fs:read:cfg:state_dir/cfg:plugin_name`? Position: **no**.
  One template segment per entry; nesting invites confusion.
- Should `expandFSEntry` log when expansion fails? Position:
  yes at warn level on the host side, but not in this EP — the
  current "silently filter" behaviour is consistent with how
  malformed caps are skipped elsewhere. A separate observability
  pass could add structured warnings.
- Should the doctor command auto-detect missing cfg pairings in
  the cap classifier? Position: yes — `plugin doctor` would render
  "fs:read:cfg:state_dir/... but cfg:state_dir is not declared"
  as a manifest warning. Future doctor enhancement; not in this
  EP.

## Decision log

### D1. Template expansion is at-check time, not at-install time

- **Decided:** the parser stores entries verbatim; expansion
  happens in `allowRead`/`allowWrite` against the host's currently-
  populated cfg fields.
- **Alternatives:** expand at-install time (resolves once when
  the plugin is installed); expand at-NewHost time (resolves once
  per host construction).
- **Why:** the host caller populates cfg fields (`host.StateDir`,
  etc.) AFTER `NewHost` returns. Install-time expansion can't
  see those values. NewHost-time expansion would require all
  callers to populate cfg fields BEFORE NewHost — a bigger API
  contract change. At-check time is the simplest invariant: the
  resolution always uses the current populated value.

### D2. Missing cfg:* cap fails the expansion silently

- **Decided:** `expandFSEntry` returns "" when the matching
  `cfg:<name>` cap is not declared; the resulting "no match"
  surfaces as "access denied" at runtime.
- **Alternatives:** loud error at parse time; loud error at check
  time; skip the cap-declaration check.
- **Why:** consistency with how other malformed/unknown caps are
  silently skipped (see the existing `net:deny` / `malformed`
  test cases). A loud error at parse time would require parsing
  cfg caps before fs caps (or a two-pass parser); at-check time is
  uniform with the existing fail-closed pattern.

### D3. Cap syntax is `fs:read:cfg:<name>/...`, not
`fs:read:${cfg.state_dir}/...` or similar template syntax

- **Decided:** the template prefix is the literal `cfg:` —
  matching the capability vocabulary it references — followed by
  `<name>` and an optional `/<sub-path>`.
- **Alternatives:** `${cfg.state_dir}/...`; `~cfg.state_dir/...`;
  `@state_dir/...`.
- **Why:** consistency with the cap vocabulary (`cfg:state_dir`
  is a cap; `fs:read:cfg:state_dir/...` reads UNDER the same
  thing). No new substitution machinery; the parser just looks
  for the `cfg:` prefix. Operators reading capabilities can
  pattern-match without learning a new template syntax.

## Related

- EP-0005 — Capability-Based Sandboxing. The discipline this EP
  follows (explicit per-cap opt-in, fail-closed expansion).
- EP-0006 — Signed WASM Plugin Runtime. The trust + install
  surface that guards which plugins can declare cfg-templated
  caps.
- EP-0029 — Config-introspection host imports. The capability
  vocabulary this EP extends. Future cfg:* names that ship
  (cfg:config_dir, cfg:worktree_dir, etc.) are usable in this
  EP's path templates the same day they ship.
