# `stado audit`

Verify and export stado’s signed session history.

## What it does

`stado audit` is the tamper-evidence surface for the sidecar repo:

- `audit verify [session-id]` walks the signed `tree` and `trace` refs
- `audit export [session-id]` emits the commit history as JSONL
- `audit pubkey` prints the local signing public key + fingerprint

If you omit `session-id`, `verify` and `export` operate on every session
in the current repo’s sidecar.

## Why it exists

The sidecar repo is meant to be more than “where the agent put its
changes.” It is also the audit trail: every tool call lands on the
`trace` ref, mutating calls land on `tree`, and both refs are signed.

`stado audit` is the operator-facing way to answer:

- has this history been tampered with?
- what exactly happened, in machine-readable form?
- what key is this stado build/session using to sign?

## Subcommands

### `audit verify [session-id]`

Walks the `tree` and `trace` refs and verifies each commit’s Ed25519
signature against the local stado signing key.

Per-ref status is printed as one line:

```text
OK|UNSIGNED|FAIL   <session>   tree|trace   N total (S signed, U unsigned, I invalid)
```

If any unsigned or invalid commit is found, the command exits non-zero.

Examples:

```sh
stado audit verify
stado audit verify <session-id>
```

### `audit export [session-id]`

Exports `tree` and `trace` history as JSON lines, newest-first per ref.
Useful for SIEM ingestion, scripting, or offline analysis.

Each line includes:

- commit hash
- ref name
- parents
- tree hash
- timestamp / author / email
- commit title
- parsed trailers
- whether the commit carried a signature trailer

```sh
stado audit export <session-id> > audit.jsonl
```

### `audit pubkey`

Prints the local agent signing public key in the form:

```text
<fingerprint>  <hex-public-key>
```

Useful when you need to inspect or compare the local trust root.

## Relationship to session refs

- `refs/sessions/<id>/tree` is the executable mutation history
- `refs/sessions/<id>/trace` is the full tool-call audit history

`audit verify` walks both because either one being modified after the
fact breaks the trust model.

## Gotchas

- `audit verify` checks the local sidecar history, not a branch you
  already landed into your user repo.
- A missing ref is skipped rather than treated as corruption; fresh
  sessions may not have both refs yet.
- `audit pubkey` prints the local signing key, not the release minisign
  trust root. For release provenance, use `stado verify`.
