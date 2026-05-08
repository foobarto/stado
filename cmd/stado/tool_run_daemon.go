package main

// `stado tool run` ↔ daemon glue. PTY-bound tools (and, in future,
// other stateful tool families like browser sessions) dispatch through
// the daemon so their state survives across CLI invocations. Non-PTY
// tools still take the in-process path — the daemon does not change
// their behaviour and we don't pay UDS round-trip latency on tools that
// don't need it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/runtime"
)

// runtimeLookupCanonical wraps runtime.LookupToolMetadata so the
// helper file can reach the metadata table without re-importing it
// from tool_run.go. Returns the canonical dotted form ("shell.spawn")
// or "" when the registered name isn't in the metadata table.
func runtimeLookupCanonical(registered string) string {
	return runtime.LookupToolMetadata(registered).Canonical
}

// daemonMode is the operator's choice of daemon involvement, sourced
// from the STADO_DAEMON env var:
//
//   - auto    (default): use the daemon when reachable; auto-spawn it
//     if a PTY-bound tool needs it.
//   - manual: use the daemon when reachable; do NOT auto-spawn — the
//     operator runs `stado daemon start` themselves.
//   - off:    never use the daemon. Stateful tools refuse with the
//     classic "single-shot CLI can't host this" message.
type daemonModeKind int

const (
	daemonModeAuto daemonModeKind = iota
	daemonModeManual
	daemonModeOff
)

// daemonMode returns the operator's STADO_DAEMON preference, defaulting
// to auto. Unrecognised values fall back to auto with no warning — the
// CLI is robust to typos in this env var; the trade-off is that a
// shadowed value won't get noticed until the operator wonders why their
// "off" setting isn't sticking. Acceptable for a feature whose default
// behaviour is what most operators want.
func daemonMode() daemonModeKind {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("STADO_DAEMON"))) {
	case "off", "no", "disabled", "0", "false":
		return daemonModeOff
	case "manual":
		return daemonModeManual
	default:
		return daemonModeAuto
	}
}

// daemonAutoSpawnTimeout caps how long `stado tool run` will wait for
// an auto-spawned daemon to come up before giving up and either
// falling back to single-shot (for non-PTY tools) or refusing (for
// PTY-bound). 2 s is generous: in practice the spawn-then-listen
// happens well under 100 ms on a warm machine.
const daemonAutoSpawnTimeout = 2 * time.Second

// dispatchViaDaemon ensures a daemon is reachable (auto-spawning if the
// mode allows), sends the tool.call, and renders the result on the
// caller's stdout/stderr. The caller is responsible for the disabled-
// tool / canonical-form lookup; the registered name passed here is
// already what the daemon's registry will see.
func dispatchViaDaemon(ctx context.Context, registered, argsJSON string, opts toolRunOptions, mode daemonModeKind) error {
	socketPath, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	stadoBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: locate stado binary: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, daemonAutoSpawnTimeout)
	defer cancel()

	var c *daemon.Client
	switch mode {
	case daemonModeManual:
		// Manual mode: don't auto-spawn. Just dial.
		cl, _, derr := daemon.DialAndHandshake(dialCtx, socketPath, "stado-tool-run")
		if derr != nil {
			return errPTYRequiresDaemon(registered,
				fmt.Sprintf("STADO_DAEMON=manual and the daemon is not reachable. Run `stado daemon start` then retry. (%v)", derr))
		}
		c = cl
	default: // auto
		cl, _, derr := daemon.EnsureRunning(dialCtx, socketPath, stadoBin, daemonAutoSpawnTimeout)
		if derr != nil {
			return errPTYRequiresDaemon(registered,
				fmt.Sprintf("daemon auto-spawn failed: %v", derr))
		}
		c = cl
	}
	defer c.Close()

	workdir := opts.Workdir
	if workdir == "" {
		if cw, werr := os.Getwd(); werr == nil {
			workdir = cw
		}
	}
	res, err := c.ToolCall(ctx, daemon.ToolCallParams{
		Tool:      registered,
		Args:      []byte(argsJSON),
		ProjectID: deriveProjectID(workdir),
		Workdir:   workdir,
		SessionID: opts.Session,
	})
	if err != nil {
		return err
	}
	if res.Error != "" {
		return fmt.Errorf("plugin error: %s", res.Error)
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	if res.Content != "" {
		fmt.Fprintln(stdout, res.Content)
	}
	return nil
}

// deriveProjectID returns a stable identifier for the working
// directory the call is rooted at. Resolution order:
//
//  1. STADO_SESSION_ID env var (operator/agent override; most
//     fine-grained — multiple cwds inside the same agent context
//     share state).
//  2. Git repository root that contains workdir, hashed.
//  3. workdir itself, hashed (non-git directories).
//
// Hashing collapses the path into a 16-hex-char identifier so the
// daemon's session listings don't leak the operator's directory tree
// to log scrapers. The hash is stable across runs for the same path
// — `stado tool run` from /home/me/proj-a always lands in the same
// scope.
func deriveProjectID(workdir string) string {
	if v := strings.TrimSpace(os.Getenv("STADO_SESSION_ID")); v != "" {
		return v
	}
	root := projectRoot(workdir)
	if root == "" {
		return ""
	}
	return hashProjectRoot(root)
}

// projectRoot walks upward from workdir looking for a .git/ marker.
// Returns the directory containing .git, or workdir itself if no git
// repo is found (or workdir is empty). Walks at most 64 parents to
// bound runtime in pathological cases.
func projectRoot(workdir string) string {
	if workdir == "" {
		return ""
	}
	dir := workdir
	for i := 0; i < 64; i++ {
		if _, err := os.Stat(dir + "/.git"); err == nil {
			return dir
		}
		parent := parentDir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return workdir
}

// parentDir returns dir's parent. Avoids importing path/filepath just
// for Dir(); the manual split is fine for the simple cases we care
// about (absolute paths with forward-slash separators on the unix
// targets the daemon ships on).
func parentDir(dir string) string {
	for i := len(dir) - 1; i > 0; i-- {
		if dir[i] == '/' {
			return dir[:i]
		}
	}
	if len(dir) > 0 && dir[0] == '/' {
		return "/"
	}
	return dir
}

// hashProjectRoot returns a 16-hex-char SHA-256 prefix for path. The
// truncation is a deliberate trade — full 64-char hashes are unwieldy
// in operator-facing UIs and 16 hex (= 64 bits) is collision-free for
// the realistic project counts a single daemon serves.
func hashProjectRoot(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// errPTYRequiresDaemon shapes the actionable error the operator sees
// when a PTY-bound tool can't dispatch through the daemon. The
// message names the canonical tool form so the operator can copy-
// paste a fix without guessing wire vs canonical naming.
func errPTYRequiresDaemon(registered, why string) error {
	canonical := registered
	if md := lookupCanonical(registered); md != "" {
		canonical = md
	}
	return errors.New(
		"tool " + canonical + " needs the stado daemon to hold PTY state across calls. " + why +
			" To enable auto-spawn unset STADO_DAEMON or set STADO_DAEMON=auto. To use a manually-managed daemon: `stado daemon start` + STADO_DAEMON=manual. To stay single-shot (and refuse PTY tools): STADO_DAEMON=off. The TUI (`stado`), MCP server (`stado mcp-server`), and agent loop (`stado run`) host PTYs without the daemon.")
}

// lookupCanonical resolves a registered (wire-form) tool name to its
// canonical dotted form via the runtime metadata table. Empty return
// means the metadata didn't have a canonical entry — caller falls back
// to printing the registered form.
func lookupCanonical(registered string) string {
	// Imported indirectly via runtime — keep this helper here so the
	// import surface of tool_run.go stays unchanged. The actual
	// lookupToolMetadata call lives in tool_run.go's existing import
	// (runtime), reachable via runtime.LookupToolMetadata at the
	// caller. We re-look-up here so callers don't have to thread the
	// metadata around.
	return runtimeLookupCanonical(registered)
}
