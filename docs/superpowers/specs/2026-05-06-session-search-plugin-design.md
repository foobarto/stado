# session.search plugin — design

**Status:** drafted 2026-05-06; autonomous design.
**Branch:** `feat/session-search-plugin`.

## Problem

Long sessions accumulate hundreds of messages. Operators / agents
need a `grep`-style way to find prior discussion of a topic without
scrolling. Today the only options are external tools or rebuilding
context manually.

## Surface

One tool, `session__search`, exposed via a bundled wasm plugin. The
plugin uses the existing `session:read` host import to fetch the
history and runs the search inside the wasm module — no new host
import needed.

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | Implementation form | **Bundled wasm plugin** at `internal/bundledplugins/modules/session_search/`. | Demonstrates `session:read` end-to-end. Native-tool path would need a new tool.Host extension to access the session bridge — extra abstraction for one caller. |
| Q2 | Tool name | `session__search` (wire form), `session.search` (dotted). | Matches stado naming convention (plugin-prefix double-underscore tool-name). |
| Q3 | Capability | `session:read`. | Already implemented; provides JSON history payload. No new cap. |
| Q4 | Search engines | **Substring + regex.** Default substring (faster, no escape pitfalls). `is_regex: true` opts into Go's `regexp` (RE2-safe, no DFA-bomb risk). | Two engines covers the 95% use case. Globs / fuzzy match deferred. |
| Q5 | Case sensitivity | Default **case-insensitive**. Opt-out via `case_sensitive: true`. | Case-insensitive is the right default for casual recall ("auth" should match "Auth" / "AUTH"). |
| Q6 | Role filter | Optional `roles: ["user","assistant","tool","tool_result","system"]`. Empty / omitted = all roles. | Lets the agent narrow to "what did I (assistant) say earlier" vs "what did the user ask for". |
| Q7 | Result shape | `{matches: [{index, role, snippet, match_offset, match_length}], total_messages, matched_messages}`. Snippet centred on the match, ±N chars (default 80). | Compact, agent-friendly. Indexable back into the full history if the agent wants to dive deeper. |
| Q8 | Result limit | Default 50; `max_results` arg, capped at 1000 (defends against runaway regex). | Bounded payload + budget. |
| Q9 | Snippet length | Default 80 chars total around the match; capped at 400. | Keeps the response compact when matches are abundant. |
| Q10 | Class | `tool.ClassNonMutating` (read-only). | Search doesn't change session state. |

## Out of scope

- Multi-session search (current session only in v1)
- Fuzzy / approximate matching
- Time-range filtering
- Tool-call argument matching (only message text)
- Saving / replaying searches

## Done definition

- Plugin source under `internal/bundledplugins/modules/session_search/`.
- Search logic factored into a non-wasip1 helper file with unit tests.
- `internal/bundledplugins/build.sh` builds `session_search.wasm`.
- Registered in `internal/runtime/bundled_plugin_tools.go` via
  `newBundledWasmTool` with cap `session:read`.
- Smoke: `./stado` → tools.search "session" returns the new tool;
  describing it shows the schema; running it on a session with
  history returns matches.
- CHANGELOG entry under `Unreleased` (will land in next release tag).
