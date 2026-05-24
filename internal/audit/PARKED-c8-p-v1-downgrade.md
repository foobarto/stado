# 2026-05-24 — Park Codex C8/P (audit v1 signature downgrade)

status: parked — needs operator design call before any fix lands

## Finding (Codex deep-dive, post-v0.54.0)

`audit.ExtractSignature` (`internal/audit/signer.go:197`) finds the
FIRST `Signature: ed25519:` line anywhere in the commit body.
Combined with `VerifyV2`'s v1-fallback policy
(`internal/audit/signer.go:247`), an attacker with sidecar write can
mount a downgrade attack:

1. Take an OLD v1-signed commit (tree T, parents P, body B,
   signature S over `CanonicalBytes(T, P, B)`).
2. Construct a NEW commit with same tree T, parents P, body B (incl.
   the v1 Signature trailer), but rewritten author / timestamp.
3. VerifyV2 tries v2 first
   (`CanonicalBytesV2(T, P, B, attackerIdent)`) — fails because
   identity is bound.
4. Falls back to v1 (`CanonicalBytes(T, P, B)`) — succeeds because
   v1 doesn't bind identity, and (T, P, B) match the original.
5. → Downgrade: attacker's commit verifies with the operator's
   pubkey.

Codex's fix proposal: scheme marker (`ed25519-v2:` prefix, or
`Signature-Version: 2` trailer), extract only from final trailer
block, reject multiple Signature trailers, **bounded v1 policy**.

## Why parked

The v0.54.0 release shipped the v2 framing (`stado-audit-v2\n...`),
`CanonicalBytesV2`, and `VerifyV2`-with-v1-fallback. v0.54.0's
CHANGELOG promises that "existing v1 signatures continue to verify
via `audit.VerifyV2`'s fallback path; new commits produce v2
signatures" and that third-party tooling should "accept BOTH framings
during the migration window."

**The codex fix as proposed breaks this** — v0.54.0-era v2 sigs have
no body marker to distinguish them from downgrade-attack targets:

- Adding a body marker (`Signature-Scheme: v2`) that gates v1 fallback
  works for NEW v2 sigs but rejects v0.54.0-era v2 sigs (no marker).
- A date-cutoff approach requires either a config or hardcoded cutoff
  — operators would have to know it.
- Adding an `ed25519-v2:` prefix to the Signature trailer value breaks
  v0.54.0-era v2 sigs (their value starts with `ed25519:` not
  `ed25519-v2:`).

## Possible designs (for operator review)

1. **Time-windowed cutoff**: refuse v1 fallback for commits whose
   committer timestamp is after a hardcoded v0.56.0 cutoff. Old
   commits keep falling back. Imperfect — attacker can backdate
   committer timestamp (not bound by v1).
2. **Body marker + grace period**: add `Signature-Scheme: v2` to newly
   signed v2 commits. Allow v1 fallback for both "no marker AND
   timestamp before cutoff" (genuine v1) AND "no marker AND timestamp
   in [v0.54.0, v0.56.0)" (would-have-been-v2 but didn't get marked).
   Post-cutoff "no marker" = strict v2-only. Still has the backdate
   issue.
3. **Pin per-repo policy**: operator runs `stado audit pin-strict-v2`
   once they've verified all history; subsequent verifies refuse v1
   fallback period. Pre-pin = current loose behavior. Most explicit;
   requires operator action.
4. **Re-sign in-place**: a `stado audit migrate-to-v2-strict` tool
   that walks the sidecar, re-signs every v0.54.0-era v2 commit with
   the marker, then enables strict mode. Most thorough; biggest
   one-time cost.

## What ships meanwhile

- v0.56.0 (PRs #62–#65) lands the other 12 deep-dive findings.
- C8/P stays in the queue; surface in the next codex/gemini pass too
  so it stays visible.
- Memory `sec_park_C8_P_v1_downgrade.md` (in operator's auto-memory,
  not the repo) mirrors this analysis for the agent's recall on
  subsequent sessions.

## Implements

Per /goal "if fixing reported vulnerability would break design choice
made earlier (if yes, park such reports for future review)" rule.
