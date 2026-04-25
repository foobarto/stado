# Plugin rollback checks need semver comparison

- Do not compare plugin manifest versions as raw strings. Lexicographic
  ordering treats `1.2.0` as newer than `1.10.0`, which weakens rollback
  protection after a trusted signer has published a higher version.
- Keep rollback comparison in the shared `internal/plugins` trust layer
  and reuse it from runtime plugin override verification, so install,
  verify, TUI `/plugin`, and runtime overrides enforce the same rule.
- Add explicit regression coverage for non-lexicographic semver ordering
  whenever touching plugin trust verification.
