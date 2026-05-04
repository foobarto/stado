# First-time-user feedback — Bazzite + LMStudio + qwen3.6-uncensored

Date: 2026-05-04
Source: A friend of the operator trying stado for the first time on
Bazzite (Atomic Fedora derivative). Forwarded as screenshots; raw
verbatim not preserved.
Audience: EP-0030 design-phase input. Use this when promoting EP-0030
from Placeholder → Draft.

## Setup observed

- **OS**: Bazzite (Fedora-Atomic-derived; `/home → /var/home`).
- **Inference**: LMStudio serving `qwen3.6-35b-a3b-uncensored-hauhaucs-aggressive`
  via OAI-compat API.
- **stado**: Pre-v0.26.0 release binary (the one with the `/home`-symlink
  boot bug; user actually hit "config: create config dir: directory
  component is a symlink: home" before getting in).

## What happened

Friend tried `stado run --tools` against the local LMStudio with a
prompt like *"run ls on /home/maly"*. Trace from the screenshots:

1. stado correctly opened a session worktree at
   `/home/maly/.local/state/stado/worktrees/848a5f0d-...` (visible
   in the CWD render footer — proof the session-pin + EP-0004 audit
   trail is working).
2. The bash tool ran `ls -la /home/maly` correctly. Returned a
   sparse listing (`.local` only — friend's home is genuinely
   sparse on this machine).
3. **The model invented a follow-up the friend didn't ask for**:
   *"No `htb` directory exists anywhere on the filesystem. Would
   you like me to create it at `~/htb`, or do you have a Git repo
   URL I should clone there?"*

Friend's reaction was confusion — "is stado doing this, or is the
model?". Forwarded the screenshots as a probable bug report.

## Diagnosis

**Not a stado bug.** stado's behaviour was correct end-to-end:

- Worktree was correctly pinned (image 2 confirms).
- bash tool ran the operator's command exactly as requested.
- The audit-trail commit landed in `refs/sessions/<id>/trace`.

**Model-quality issue** — `qwen3.6-35b-a3b-uncensored-hauhaucs-aggressive`
is a 35B-param uncensored fine-tune tuned for "aggressive"
behaviour. Those tunes:
- Hallucinate plausible-sounding next steps (the `htb` invention).
- Ignore narrow user prompts in favour of broader interpretation
  ("you ran ls; surely you wanted me to set something up").
- Are a poor first-impression model for any agent harness, not
  just stado.

Boot bug on Bazzite was real and fixed in v0.26.0 (the Atomic-
Fedora `/home → /var/home` story documented in EP-0028).

## What this surfaces for EP-0030

The friend's experience maps directly onto EP-0030's "security-
research default harness" goals:

1. **Recommended-model floor.** A first-time stado user with a
   weak local model gets a poor first impression — even when stado
   is doing everything right. EP-0030's "behavioural conventions
   doc" should include guidance on which model classes are below
   the floor (e.g., 7B/13B uncensored fine-tunes; small "creative"
   fine-tunes; quantised models below 4-bit; etc.) and which are
   safe defaults (Anthropic claude-haiku-4-5 / claude-sonnet-4-6,
   OpenAI gpt-5, frontier-class Ollama Cloud models like kimi-k2.6
   / glm-5.1 / minimax-m2.7).

2. **First-time-run sanity check.** Could be a `stado doctor`
   extension: detect that the configured `[defaults].provider` is
   pointing at a small / known-low-quality model and emit a one-
   line warning at startup. Not a hard fail — just a "fyi this
   model is below our recommended floor; expect quality
   surprises". Concrete trigger for the friend's case: any model
   whose name contains `uncensored` AND whose param count (parsed
   from name when present) is < 70B.

3. **Bug-bounty + pen-test framing**: the friend's choice of
   "uncensored" model signals exactly the EP-0030 audience.
   Operators in the security-research workflow specifically want
   uncensored models because mainstream-aligned ones refuse on
   the way to "let me show you how to extract this credential
   for the box you're authorised to root". The doctor warning
   above must NOT presume "uncensored = bad"; it should presume
   "uncensored AND small = bad-for-agent-harness, regardless of
   workflow".

4. **Worktree-vs-CWD confusion.** Image 2 shows
   `Current working directory: /home/maly/.local/state/stado/worktrees/848a5f0d-...`
   in the footer — accurate but possibly confusing for first-time
   users who don't know what a "session worktree" is. EP-0030's
   onboarding could include a one-paragraph "what is the worktree
   and why am I in it" explainer surfaced on first session.

5. **`/home`-symlink boot bug** caught a real user — exactly the
   Atomic-Fedora-derived audience EP-0028's HOME-rooted MkdirAll
   work targeted. v0.26.0 ships the fix; the friend should
   rebuild to get past the boot wall.

## Recommended FTU defaults (suggested for EP-0030)

If stado were to ship a security-research default harness, these
would be reasonable opt-in defaults for the FTU experience:

- **Recommended model fallback table** in `stado doctor` — when
  no model is configured, suggest Anthropic claude-haiku-4-5 (best
  default), with notes on Anthropic key acquisition.
- **`stado run --tools` startup blurb** when the configured model
  is below the recommended floor: one-line "fyi this is a 35B
  uncensored model; expect hallucinated next steps. For better
  first-time experience, see [link to EP-0030 model floor doc]."
- **Worktree explainer** on first invocation: a one-time
  `~/.local/share/stado/.first-run-seen` marker; without it, print
  a brief "stado opened a session worktree at X — your project
  files are still at Y; the worktree is where the agent's
  scratch + audit trail live." Suppressed forever after first run.

None of these ship in v0.26.0. All three are concrete EP-0030
slices once that EP transitions Placeholder → Draft.

## Coordination note

The other Claude process (active in `~/Dokumenty/stado` working
tree) is also working from the same FTU pain — the
`stado run --quiet` flag from `ca95b97` directly addresses the
"interleaved tool-call previews make stdout hard to script-parse"
pain that an FTU script-driving stado would hit. Worth coordinating
EP-0030 design across whichever Claude session ends up writing the
Draft — the dogfood corpus across both is the actual input.

## See also

- [EP-0028](../eps/0028-plugin-run-tool-host.md) §"Boots on
  Atomic Fedora" — the underlying boot fix.
- [EP-0030](../eps/0030-security-research-default-harness.md) —
  the placeholder this report feeds.
- [`~/Dokumenty/bounty-hunting/`](file:///home/foobarto/Dokumenty/bounty-hunting/)
  + [`~/Dokumenty/htb-writeups/`](file:///home/foobarto/Dokumenty/htb-writeups/)
  — primary inspiration sources for the EP-0030 Draft phase.
