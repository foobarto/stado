The first assistant text render used `glamour.WithAutoStyle()`.
Glamour resolves the "auto" style through `termenv.HasDarkBackground`,
which sends OSC 10/11 plus a cursor-position query to the terminal and
then reads from the same TTY Bubble Tea is using for input.

In the TUI this shows up exactly at the thinking -> answer transition:
thinking uses a plain wrap template, but the first assistant text delta
creates the markdown renderer. The UI can pause for the OSC timeout or
until the terminal replies, and Bubble Tea may receive fragments like
`e/1e1e/1e1e\` as typed input.

Fix pattern:

- never use `glamour.WithAutoStyle()` inside the Bubble Tea render path
- choose a deterministic style, e.g. `glamour.WithStandardStyle(styles.DarkStyle)`
- keep OSC input filtering for stale terminal replies, including
  ragged color tails where the `rgb:` prefix was already consumed
