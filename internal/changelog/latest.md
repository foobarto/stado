## v0.80.0 — harness-enforced supervised work — 2026-08-13

### UX / CLI / TUI

- **`/supervise` makes long-running work a host-enforced contract.** A trusted
  basic/advanced wizard gathers requirements through a fresh watchdog and waits
  for explicit baseline approval. Event mode reviews native warning signals by
  default; live mode follows every turn. Anchored watchdog corrections can
  interrupt or pause the worker, unrelated follow-ups become shared tasks, plan
  pivots respect user/watchdog policy, human-only risk boundaries stop in a
  trusted drawer, and a separate fresh verifier owns completion after Verify
  Work gates. Reviewer citations bind to evidence actually served from bounded
  session/activity and immutable repository views; all approved plan steps must
  finish first. Durable `status`, `resume`, and `cancel` survive restarts, and
  automatic context-recovery forks retain the immutable root while atomically
  advancing the attached worker-session anchor.
- **Supervision tradeoffs are repeatable.** `stado supervise-eval` validates six
  local/Ollama Cloud quirk scenarios and scores paired JSONL observations for
  criteria, defects, interventions, repetition, diff scope,
  escalation/completion, per-role tokens, latency, and quality per token.

### Runtime

- **Deferred user follow-ups become shared tasks by default.** The default agent
  instructions now preserve one active task, capture unrelated prompts in the
  shared task store, and revisit only that conversation's deferred task IDs
  after completion. The `tasks` tool is autoloaded so this policy remains
  actionable without tool discovery; unavailable tools and failed writes
  explicitly retain and disclose an unpersisted fallback instead of losing work.

### Docs

- **EP-0062 documents supervised-work authority and failure semantics.** Added
  the feature/security/architecture/command guides, linked the Verify Work and
  native-guidance predecessor EPs, and reconciled the README, landing-page reel,
  roadmap, UAT catalogue, and evaluation protocol.

- **Task-store maturity boundaries are explicit.** Documented the deferred-work
  convention and clarified that the current global CRUD store is not yet a
  repository-scoped scheduler or automatic dispatcher.
- **WASM plugins join the feature reel.** Promoted stado's replaceable
  plugin-first tool architecture, capability-gated WASM runtime, shared ABI,
  distinct embedded-bundled versus signed-installed trust paths, and the
  remaining native `tasks` bootstrap exception in the README and static landing
  page.

