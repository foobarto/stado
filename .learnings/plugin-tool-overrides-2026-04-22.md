# Plugin tool overrides

The first practical EP-2 slice is to treat plugin replacement as a registry concern, not a TUI concern.

1. Add per-tool class to the signed plugin manifest. Without that, an override inherits the bundled tool name but loses the correct tree/trace commit policy.

2. Load and verify override plugins once at startup, but instantiate them lazily per tool call. That avoids having to thread wazero runtime cleanup through every executor lifetime immediately, while still allowing installed plugins to replace bundled tools by registry name.

3. Invalid overrides should fail startup loudly. Silently falling back to the native tool defeats the whole point when the override is there for policy, auditing, or scrubbing.
