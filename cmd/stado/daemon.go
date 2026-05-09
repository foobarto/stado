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
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
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

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("daemon: load config: %w", err)
	}
	state, err := newDaemonState(cfg)
	if err != nil {
		return fmt.Errorf("daemon: build state: %w", err)
	}
	defer state.Close()

	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath:   socketPath,
		IdleTimeout:  daemonStartIdle,
		Logger:       logger,
		Dispatcher:   state.dispatch,
		ListSessions: state.listSessions,
		KillSession:  state.killSession,
		ListTools:    state.listTools,
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

// daemonState owns the long-lived state the daemon shares across tool
// calls. The single registry + executor + sandbox runner are reused
// for every dispatch; project-scoped state (PTY manager) lives in
// per-project sub-scopes so a session created in project A is
// invisible to a call from project B.
//
// Built once at `stado daemon start` and closed on shutdown. Each
// tool.call resolves a project scope (creating one on first sight),
// builds a per-call host pinned to the call's workdir + sharing that
// project's pty.Manager, and runs through Executor for audit + sandbox.
//
// The empty project_id ("") gets its own scope just like any other
// — clients that don't pass project_id are isolated from clients that
// do. Mixed-mode usage isn't a real failure, but it's not a feature
// we encourage; the default tool_run path always sends project_id.
type daemonState struct {
	cfg      *config.Config
	registry *tools.Registry
	executor *tools.Executor
	runner   sandbox.Runner

	// projectMu guards projects map. Each *projectScope holds its own
	// pty.Manager + future-state (browser cookies, LSP) — phase-3
	// version is PTY-only.
	projectMu sync.Mutex
	projects  map[string]*projectScope
}

// projectScope is the per-project state container. PTY sessions live
// here; tools dispatched against project A see only manager-A's ids.
type projectScope struct {
	id  string
	pty *pty.Manager
}

func newDaemonState(cfg *config.Config) (*daemonState, error) {
	reg, err := runtime.BuildRegistryWithPlugins(cfg)
	if err != nil {
		return nil, err
	}
	runner := sandbox.Detect()
	st := &daemonState{
		cfg:      cfg,
		registry: reg,
		runner:   runner,
		projects: make(map[string]*projectScope),
	}
	st.executor = &tools.Executor{
		Registry: reg,
		Session:  nil, // daemon dispatch isn't bound to a stadogit session.
		Runner:   runner,
		Metrics:  telemetry.Metrics{},
		Agent:    "stado-daemon",
		Model:    cfg.Defaults.Model,
		ReadLog:  nil,
	}
	return st, nil
}

// scopeFor returns (and lazily creates) the project scope for the given
// id. Caller must not retain the returned pointer past the next
// projectMu acquisition — Close() empties the map.
func (d *daemonState) scopeFor(projectID string) *projectScope {
	d.projectMu.Lock()
	defer d.projectMu.Unlock()
	if sc, ok := d.projects[projectID]; ok {
		return sc
	}
	sc := &projectScope{id: projectID, pty: pty.NewManager()}
	d.projects[projectID] = sc
	return sc
}

// allScopes returns a snapshot of all scopes so iteration callers
// don't hold projectMu across pty.Manager calls (which themselves take
// internal locks).
func (d *daemonState) allScopes() []*projectScope {
	d.projectMu.Lock()
	defer d.projectMu.Unlock()
	out := make([]*projectScope, 0, len(d.projects))
	for _, sc := range d.projects {
		out = append(out, sc)
	}
	return out
}

func (d *daemonState) Close() {
	if d == nil {
		return
	}
	for _, sc := range d.allScopes() {
		if sc.pty != nil {
			sc.pty.CloseAll()
		}
	}
}

// dispatch is the daemon-side ToolCall handler. Resolves the tool name
// (same fallback chain as `stado tool run`), enforces [tools].disabled
// patterns from config, builds a per-call host pinned to the call's
// workdir + the project scope's pty.Manager, and runs through Executor.
//
// Disabled-tool enforcement runs server-side because the operator's
// config.toml is the authoritative policy — a misbehaving client can't
// bypass [tools].disabled by not checking it client-side. Per-call
// AllowList enforcement happens earlier inside daemon.Server before
// the dispatcher sees the call (see server.go handleToolCall).
func (d *daemonState) dispatch(ctx context.Context, p daemon.ToolCallParams) (daemon.ToolCallResult, error) {
	registered, ok := lookupToolInRegistry(d.registry, p.Tool)
	if !ok {
		return daemon.ToolCallResult{}, fmt.Errorf("tool %q not found", p.Tool)
	}
	if d.cfg != nil {
		registeredName := registered.Name()
		canonical := runtime.LookupToolMetadata(registeredName).Canonical
		for _, pat := range d.cfg.Tools.Disabled {
			if runtime.ToolMatchesGlob(registeredName, pat) ||
				(canonical != "" && runtime.ToolMatchesGlob(canonical, pat)) {
				return daemon.ToolCallResult{}, fmt.Errorf(
					"tool %q is disabled in [tools].disabled (matched pattern %q)", p.Tool, pat)
			}
		}
	}
	workdir := p.Workdir
	if workdir == "" {
		// Sensible fallback: the daemon's cwd at start. The CLI
		// normally fills this in from the calling process's cwd.
		if cw, err := os.Getwd(); err == nil {
			workdir = cw
		} else {
			workdir = "."
		}
	}
	scope := d.scopeFor(p.ProjectID)
	host := &daemonToolHost{
		workdir: workdir,
		runner:  d.runner,
		pty:     scope.pty,
	}
	args := json.RawMessage(p.Args)
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	res, err := d.executor.Run(ctx, registered.Name(), args, host)
	if err != nil {
		// Forward the executor error string but also keep tool.Result's
		// own Error so the client gets the richer message when present.
		msg := err.Error()
		if res.Error != "" {
			msg = res.Error
		}
		return daemon.ToolCallResult{Content: res.Content, Error: msg}, nil
	}
	return daemon.ToolCallResult{Content: res.Content, Error: res.Error}, nil
}

// listSessions reports live PTY sessions. ProjectID="" + all=false
// means "the unscoped scope only"; ProjectID="" + all=true sweeps every
// project. ProjectID="X" returns only project X's sessions regardless
// of all (project-explicit always wins; preserves the operator-facing
// invariant that listing a project shows that project, period).
func (d *daemonState) listSessions(projectID string, all bool) []daemon.SessionDescriptor {
	scopes := d.allScopes()
	out := make([]daemon.SessionDescriptor, 0)
	for _, sc := range scopes {
		if !all && sc.id != projectID {
			continue
		}
		if sc.pty == nil {
			continue
		}
		for _, in := range sc.pty.List() {
			out = append(out, daemon.SessionDescriptor{
				Kind:      "pty",
				ID:        in.ID,
				Summary:   in.Cmd,
				Alive:     in.Alive,
				StartedAt: in.StartedAt,
				ProjectID: sc.id,
			})
		}
	}
	return out
}

// killSession destroys (kind, id) within the named project. Cross-
// project kills are refused by ProjectID mismatch (the lookup pulls a
// scope by id; an unknown id silently returns false). Idempotent on
// not-found — a caller racing two kills against the same session
// shouldn't see a hard error from the loser.
func (d *daemonState) killSession(p daemon.SessionKillParams) (bool, error) {
	if p.Kind != "pty" {
		return false, nil
	}
	d.projectMu.Lock()
	scope, ok := d.projects[p.ProjectID]
	d.projectMu.Unlock()
	if !ok || scope.pty == nil {
		return false, nil
	}
	if err := scope.pty.Destroy(p.ID); err != nil {
		return false, nil
	}
	return true, nil
}

// listTools returns the daemon's current tool catalogue. The returned
// schema is the JSON-marshalled form of each tool's Schema() map.
func (d *daemonState) listTools() []daemon.ToolDescriptor {
	all := d.registry.All()
	out := make([]daemon.ToolDescriptor, 0, len(all))
	for _, t := range all {
		md := runtime.LookupToolMetadata(t.Name())
		schema, _ := json.Marshal(t.Schema())
		out = append(out, daemon.ToolDescriptor{
			Name:        t.Name(),
			Canonical:   md.Canonical,
			Description: t.Description(),
			Schema:      schema,
			Class:       d.registry.ClassOf(t.Name()).String(),
		})
	}
	return out
}

// daemonToolHost is the tool.Host the dispatcher hands to Executor.Run.
// Auto-approve (the calling client is the authorisation boundary —
// same posture as stado mcp-server), per-call workdir, daemon-shared
// pty.Manager. PriorRead/RecordRead are no-ops; per-call dedup makes
// little sense for daemon-served calls (each `stado tool run` is its
// own caller, so deduping the read across them would surprise).
type daemonToolHost struct {
	workdir string
	runner  sandbox.Runner
	pty     *pty.Manager
}

func (h *daemonToolHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h *daemonToolHost) Workdir() string                                 { return h.workdir }
func (h *daemonToolHost) Runner() sandbox.Runner                          { return h.runner }
func (h *daemonToolHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) { return tool.PriorReadInfo{}, false }
func (h *daemonToolHost) RecordRead(tool.ReadKey, tool.PriorReadInfo)       {}
func (h *daemonToolHost) PTYManager() any                                  { return h.pty }

// DefaultSandboxPolicy implements tool.SandboxPolicyProvider — plugins
// calling stado_exec / stado_proc_spawn through the daemon get the
// host-default protective policy (bwrap / sandbox-exec PID + uid
// namespace isolation) without having to supply their own `sandbox`
// field. Matches the mcp-server posture; both surfaces hand tool calls
// to autonomous agents that we want confined by default.
//
// stado run + stado tool run + TUI deliberately do NOT set this — the
// operator is invoking explicitly there and the legacy "operator's
// filesystem" semantics still apply.
func (h *daemonToolHost) DefaultSandboxPolicy() any {
	return pluginRuntime.NewDefaultSandboxPolicy(h.workdir)
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
