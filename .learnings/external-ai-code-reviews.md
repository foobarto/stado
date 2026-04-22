# Running External AI Code Reviews

When the project is in a mature state or before major commits, run both Claude Code and Codex for independent quality scans.

## Prerequisites

- `claude` CLI (v2+) — Anthropic account, `claude login` to authenticate
- `codex` CLI (OpenAI) — `codex login`, uses GPT-5.4 by default
- Both must be authenticated before use

## Prompt

Use the prompt in `/tmp/review-prompt.txt` or recreate it:

```
Review the Go project stado (AI coding-agent TUI/CLI) for overall quality.
Focus areas (most to least important):
1. Concurrent data races, resource leaks, or goroutine hazards
2. Error handling patterns — are errors lost silently? Are they actionable?
3. Security: plugin sandboxing, trust model, secret handling
4. Architecture: dependency direction, leaking abstractions, coupling
5. Testing gaps — obvious missing coverage or brittle assertions
6. Performance: allocation hotspots, unnecessary re-rendering
7. UX polish: error messages, empty states, discoverability
For each finding, give:
- Severity: P1 (bug/risk), P2 (rough edge), P3 (nit)
- File and approximate line range
- One-sentence impact
- One-sentence suggested fix (no need to write code)
Limit to ~15 findings. Be specific and actionable, not vague.
```

## Commands

### Codex (OpenAI)
```
codex --full-auto exec review "$(cat /tmp/review-prompt.txt)"
```
- `--full-auto`: never prompts for approval
- `exec review`: non-interactive code review subcommand
- Output goes to stdout; redirect to file for comparison

### Claude (Anthropic)
```
claude --permission-mode bypassPermissions -p "$(cat /tmp/review-prompt.txt)"
```
- `--permission-mode bypassPermissions`: skip interactive permission checks
- `-p` or `--print`: non-interactive output mode
- May also work with `--bare` flag for minimal context

## Validation

Always verify tools work first:
```
claude --permission-mode bypassPermissions -p "What is 2+2?"
codex --full-auto exec "What is 2+2?"
```

If either fails with auth errors, run `claude login` or `codex login`.

## Comparison

- **Codex** tends to find concurrency/race issues aggressively (Go-specific)
- **Claude** tends to find security and architectural issues (broader scope)
- Both miss things the other catches; running both and merging findings is valuable
- Save outputs to `/tmp/codex-review.out` and `/tmp/claude-review.out` for later reference

## When to Run

- Before any commit touching concurrent code
- Before release tagging
- After any major refactor
- When the user explicitly asks for a "quality scan"
