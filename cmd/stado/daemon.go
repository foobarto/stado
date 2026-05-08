package main

// `stado daemon` — long-running peer that holds stateful tool state
// (PTY sessions, browser cookie jars, LSP connections) so successive
// `stado tool run` invocations see a consistent view across calls.
//
// Subcommands:
//
//	stado daemon start [--quiet] [--idle-timeout=30m] [--socket=PATH]
//	stado daemon stop  [--force]
//	stado daemon status [--json]
//
// The daemon exposes a Unix domain socket at $XDG_RUNTIME_DIR/stado/
// daemon.sock (override with $STADO_DAEMON_SOCKET) speaking newline-
// delimited JSON-RPC 2.0. `stado tool run` auto-spawns the daemon via
// `stado daemon start --quiet` when no socket is present; this file
// implements both the spawned target and the operator-facing CLI.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/daemon"
)

var (
	daemonStartQuiet      bool
	daemonStartIdle       time.Duration
	daemonStartSocketPath string

	daemonStopForce bool

	daemonStatusJSON  bool
	daemonStatusSocket string
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Long-running stado peer for stateful tool calls",
	Long: "stado daemon hosts the state that single-shot `stado tool run` calls\n" +
		"would otherwise lose between invocations: live PTYs from shell.spawn,\n" +
		"browser cookie jars, LSP connections, cached wasm modules.\n\n" +
		"Auto-spawn: `stado tool run` looks for a running daemon and starts\n" +
		"one if absent (silently in the background). For manual control use\n" +
		"`stado daemon start` / `stop` / `status`.\n\n" +
		"Socket location: $STADO_DAEMON_SOCKET, else $XDG_RUNTIME_DIR/stado/\n" +
		"daemon.sock on Linux, else $TMPDIR/stado-<uid>/daemon.sock. Mode 0700.",
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon (foreground unless --quiet)",
	Long: "Bind the UDS socket and serve until Ctrl+C, signal, idle timeout,\n" +
		"or `stado daemon stop`. With --quiet the process detaches and runs\n" +
		"in the background — that's the auto-spawn path that `stado tool run`\n" +
		"uses internally.",
	RunE: runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon",
	Long: "Send daemon.shutdown over the UDS socket and wait briefly for the\n" +
		"daemon to exit. With --force, in-flight calls are not waited on.",
	RunE: runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print daemon status (uptime, sessions, idle time)",
	RunE:  runDaemonStatus,
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd, daemonStopCmd, daemonStatusCmd)

	daemonStartCmd.Flags().BoolVarP(&daemonStartQuiet, "quiet", "q", false,
		"Detach + suppress stdout. Used by auto-spawn from `stado tool run`.")
	daemonStartCmd.Flags().DurationVar(&daemonStartIdle, "idle-timeout", daemon.DefaultIdleTimeout,
		"Exit after this much idle time (zero live sessions, zero in-flight calls). 0 = no timeout.")
	daemonStartCmd.Flags().StringVar(&daemonStartSocketPath, "socket", "",
		"UDS path to bind (default: $STADO_DAEMON_SOCKET or $XDG_RUNTIME_DIR/stado/daemon.sock)")

	daemonStopCmd.Flags().BoolVar(&daemonStopForce, "force", false,
		"Don't wait for in-flight tool calls; abort them.")

	daemonStatusCmd.Flags().BoolVar(&daemonStatusJSON, "json", false,
		"Print status as a JSON object (machine-readable).")
	daemonStatusCmd.Flags().StringVar(&daemonStatusSocket, "socket", "",
		"UDS path to query (default: $STADO_DAEMON_SOCKET or platform default)")

	rootCmd.AddCommand(daemonCmd)
}

func runDaemonStart(cmd *cobra.Command, _ []string) error {
	socketPath := daemonStartSocketPath
	if socketPath == "" {
		p, err := daemon.SocketPath()
		if err != nil {
			return err
		}
		socketPath = p
	}

	// Stale-socket guard: if the previous daemon died ungracefully and
	// left a stale socket file, RemoveStaleSocket cleans it up. If a
	// LIVE daemon is on it, we fail fast — operator runs `stop` first.
	if _, err := daemon.RemoveStaleSocket(socketPath); err != nil {
		if errors.Is(err, daemon.ErrSocketInUse) {
			return err
		}
		return err
	}

	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	var logger = stderr
	if daemonStartQuiet {
		// Auto-spawn path: the parent's stdio handles are inherited but
		// nobody is reading them. Send the daemon's log to a per-uid
		// log file so post-mortem inspection still works.
		f, err := openDaemonLog(socketPath)
		if err != nil {
			return fmt.Errorf("daemon: open log: %w", err)
		}
		logger = f
		// Drop the controlling terminal in the most portable way the
		// stdlib offers: redirect stdio to /dev/null. Setsid was
		// applied at spawn time by the caller (autospawn_unix.go).
		if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
			os.Stdin = devnull
			os.Stdout = devnull
			os.Stderr = devnull
		}
	}

	// Phase 1: scaffolding only — the daemon accepts connections and
	// responds to handshake/status/shutdown but tool.call routes through
	// a stub Dispatcher that returns a not-yet-wired error. Phase 2
	// swaps this for the real plugin runtime.
	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath:  socketPath,
		IdleTimeout: daemonStartIdle,
		Logger:      logger,
		Dispatcher:  daemonDispatcher,
		ListSessions: func(_ string, _ bool) []daemon.SessionDescriptor {
			return nil
		},
		ListTools: func() []daemon.ToolDescriptor { return nil },
	})

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if !daemonStartQuiet {
		fmt.Fprintf(stdout, "stado daemon: listening on %s (pid=%d)\n", socketPath, os.Getpid())
		fmt.Fprintf(stdout, "stado daemon: idle timeout = %s; Ctrl-C to stop\n", daemonStartIdle)
	}

	return srv.Serve(ctx)
}

func runDaemonStop(_ *cobra.Command, _ []string) error {
	socketPath, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := daemon.DialAndHandshake(ctx, socketPath, "stado-daemon-stop")
	if err != nil {
		return fmt.Errorf("stado daemon: not running (or unreachable): %w", err)
	}
	defer c.Close()
	if err := c.Shutdown(ctx, daemonStopForce, "operator"); err != nil {
		return err
	}
	fmt.Println("stado daemon: shutdown requested")
	return nil
}

func runDaemonStatus(cmd *cobra.Command, _ []string) error {
	socketPath := daemonStatusSocket
	if socketPath == "" {
		p, err := daemon.SocketPath()
		if err != nil {
			return err
		}
		socketPath = p
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	c, _, err := daemon.DialAndHandshake(ctx, socketPath, "stado-daemon-status")
	if err != nil {
		// Distinguish "not running" from real connection errors so
		// scripts can rely on the exit code.
		if isConnectionRefused(err) {
			fmt.Fprintln(cmd.ErrOrStderr(), "stado daemon: not running")
			os.Exit(3)
		}
		return err
	}
	defer c.Close()
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if daemonStatusJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "socket\t%s\n", st.SocketPath)
	fmt.Fprintf(tw, "pid\t%d\n", st.DaemonPID)
	fmt.Fprintf(tw, "stado\t%s\n", st.StadoVersion)
	fmt.Fprintf(tw, "protocol\t%s\n", st.ServerVersion)
	fmt.Fprintf(tw, "uptime\t%s\n", time.Duration(st.UptimeSec)*time.Second)
	fmt.Fprintf(tw, "idle\t%s (timeout %s)\n", time.Duration(st.IdleSec)*time.Second, time.Duration(st.IdleTimeoutSec)*time.Second)
	fmt.Fprintf(tw, "sessions\t%d live\n", st.LiveSessions)
	fmt.Fprintf(tw, "calls\t%d total\n", st.TotalCalls)
	return tw.Flush()
}

// daemonDispatcher is the Phase-1 stub Dispatcher. It rejects every
// tool.call with a clear message — Phase 2 replaces this with the
// plugin runtime + shared pty.Manager. The stub is intentionally not
// silent: a client that sees this error knows the daemon is up but
// the state-service wiring isn't finished.
func daemonDispatcher(_ context.Context, p daemon.ToolCallParams) (daemon.ToolCallResult, error) {
	return daemon.ToolCallResult{}, fmt.Errorf("daemon: tool.call %q not yet wired (phase 2)", p.Tool)
}

// openDaemonLog returns a writable log file under the same dir as the
// socket, named daemon.log. The file is created/appended at mode 0600.
// We deliberately don't rotate — operators who want rotation pipe to
// logger or set up a logrotate config; rotation in v1 isn't worth the
// complexity.
func openDaemonLog(socketPath string) (*os.File, error) {
	dir := socketPath
	for len(dir) > 1 && dir[len(dir)-1] != '/' {
		dir = dir[:len(dir)-1]
	}
	if len(dir) > 1 && dir[len(dir)-1] == '/' {
		dir = dir[:len(dir)-1]
	}
	return os.OpenFile(dir+"/daemon.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func isConnectionRefused(err error) bool {
	// We want this lightweight + dependency-free. The dial-failure
	// case from the daemon package wraps the underlying syscall, and
	// errors.Is across syscall.Errno works for refused/no-such-file
	// without us needing to import syscall on every platform.
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}
