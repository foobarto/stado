// Package hooks runs user-provided shell commands at lifecycle
// boundaries of the TUI (and, later, the headless loop). Scope is
// deliberately minimal: notification-only commands, 5-second cap, no
// ability to block or mutate the surrounding turn. A richer "approve
// tool call via external policy" version can grow on top of this
// skeleton once the simple case is proven in the wild.
//
// The hook binary is invoked as `/bin/sh -c <cmd>` with:
//   - JSON payload on stdin
//   - process env inherited from stado
//   - stdout + stderr written to stado's own stderr (prefixed so
//     noisy hooks are recognisable in a TUI session)
//
// A hook that errors (non-zero exit, timeout, or /bin/sh missing) is
// logged but never propagated — the turn that produced the event is
// already complete; the hook is a bystander.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/foobarto/stado/internal/limitedio"
)

const maxHookOutputBytes = 16 << 10

// PostTurnPayload is the JSON body piped to a post_turn hook. The
// shape is intentionally stable so user scripts can parse with jq.
// New fields are appended here; consumers should treat unknown fields
// as tolerable.
type PostTurnPayload struct {
	Event       string  `json:"event"` // always "post_turn"
	TurnIndex   int     `json:"turn_index"`
	TokensIn    int     `json:"tokens_in"`
	TokensOut   int     `json:"tokens_out"`
	CostUSD     float64 `json:"cost_usd"`
	TextExcerpt string  `json:"text_excerpt"` // first ~200 chars of assistant text
	DurationMS  int64   `json:"duration_ms"`  // wall time of the turn in ms
}

// Runner is the single entry point. Callers construct one per TUI
// program and call Post*() methods at the appropriate boundary. Zero
// value is a no-op runner so callers can always call methods without
// a nil check.
type Runner struct {
	// PostTurnCmd is the /bin/sh -c argument for the post_turn event.
	// Empty disables the hook.
	PostTurnCmd string
	// Disabled suppresses execution even when PostTurnCmd is set. The TUI uses
	// this to keep hooks from bypassing a configuration that removed `bash`
	// from the active tool set.
	Disabled bool
	// Logger receives one line per hook attempt (start + result).
	// Defaults to os.Stderr.
	Logger io.Writer
	// Timeout caps each hook's wall-clock. Zero means 5s.
	Timeout time.Duration
}

// FirePostTurn runs the post_turn hook (if configured) with the given
// payload piped to stdin. Errors are logged, never returned — the
// turn is over; a broken hook shouldn't poison the next one.
func (r *Runner) FirePostTurn(ctx context.Context, p PostTurnPayload) {
	if r == nil || r.PostTurnCmd == "" || r.Disabled {
		return
	}
	body, err := json.Marshal(p)
	if err != nil {
		r.log("hook: marshal post_turn payload: %v", err)
		return
	}
	r.exec(ctx, r.PostTurnCmd, body, "post_turn")
}

func (r *Runner) exec(ctx context.Context, shellCmd string, stdin []byte, label string) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", shellCmd) // #nosec G204 -- hook commands are explicit user configuration.
	cmd.Stdin = bytes.NewReader(stdin)
	out := limitedio.NewBuffer(maxHookOutputBytes)
	errBuf := limitedio.NewBuffer(maxHookOutputBytes)
	cmd.Stdout = out
	cmd.Stderr = errBuf
	// WaitDelay forces cmd.Run to return promptly after the context
	// is cancelled, even when a grand-child (e.g. /bin/sh's child
	// process) keeps the output pipes open. Without this, a hook
	// that spawns `sleep 5` would make cmd.Run block for the full 5s
	// even after the 100ms context cap fired — the race-enabled CI
	// reliably reproduced this. The extra 200ms after context expiry
	// is the graceful window before Go force-closes the fds.
	cmd.WaitDelay = 200 * time.Millisecond

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start).Milliseconds()

	// Emit stdout / stderr with a prefix so they don't get confused
	// for stado's own output in a shared terminal. A human looking
	// at stderr can tell "stado[hook:post_turn]" from stado's own
	// warnings.
	if out.Len() > 0 || out.Truncated() {
		fmt.Fprintf(r.writer(), "stado[hook:%s] stdout: %s", label, hookOutputString(out, "stdout"))
	}
	if errBuf.Len() > 0 || errBuf.Truncated() {
		fmt.Fprintf(r.writer(), "stado[hook:%s] stderr: %s", label, hookOutputString(errBuf, "stderr"))
	}
	if runErr != nil {
		r.log("hook %s exited after %dms with err: %v", label, dur, runErr)
	}
}

func hookOutputString(buf *limitedio.Buffer, label string) string {
	s := buf.String()
	if buf.Truncated() {
		if s != "" {
			s = trimTail(s)
		}
		s += fmt.Sprintf("[truncated: hook %s exceeded %d bytes]\n", label, maxHookOutputBytes)
	}
	return trimTail(s)
}

func (r *Runner) writer() io.Writer {
	if r.Logger != nil {
		return r.Logger
	}
	return os.Stderr
}

func (r *Runner) log(format string, args ...any) {
	fmt.Fprintf(r.writer(), "stado[hook] "+format+"\n", args...)
}

// trimTail guarantees the prefix + body line ends in a newline but
// doesn't double-up if the hook already wrote one.
func trimTail(s string) string {
	if len(s) == 0 {
		return "\n"
	}
	if s[len(s)-1] != '\n' {
		return s + "\n"
	}
	return s
}
