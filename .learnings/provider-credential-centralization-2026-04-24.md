# Provider credential lookup belongs in `internal/config`

The duplication was spread across direct provider constructors, TUI
builder logic, and `stado doctor`. The lowest-friction cleanup is not a
big resolver framework; it is a single source of truth for:

- provider name -> conventional API-key env var
- bundled OAI-compatible provider name -> default endpoint + env var

That keeps the existing call sites simple while fixing the easy drift:
user-defined presets that override bundled hosted provider names (for
example `groq` or `openrouter`) can still pick up the right API key once
`buildProvider` consults the shared config helper.
