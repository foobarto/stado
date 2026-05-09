// Package pty owns the host-side registry of PTY-backed processes
// that wasm plugins drive through the stado_pty_* host imports.
//
// One Manager per stado runtime. PTYs survive plugin instantiation
// freshness — they live in this package's goroutines and file
// descriptors, identified by uint64 id. Plugins reference them by id
// across calls; the wasm side is stateless.
//
// Lifecycle: a session is created detached, runs until the child
// process exits or someone calls Destroy. While detached, output is
// captured in a per-session ring buffer (default 64 KiB) so a later
// Attach can replay what was missed. Overflow drops oldest bytes and
// increments a counter visible via List.
package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

const (
	defaultBufferBytes = 64 * 1024
	minBufferBytes     = 4 * 1024
	maxBufferBytes     = 4 * 1024 * 1024
	destroyGrace       = 2 * time.Second
)

// closedReapGrace is the per-Manager grace between child-exit and
// watchdog-eligible reap. Set from ManagerOpts.ClosedReapGrace at
// construction; defaultClosedReapGrace when unset.
//
// Manager is a thread-safe registry of live PTY sessions.
//
// Optional idle-watchdog: when ManagerOpts.IdleTimeout > 0, a
// background goroutine periodically destroys sessions whose
// lastClientTouch is older than the threshold. Only client-driven
// activity (Read/Write/Snapshot/Attach/Detach/Resize/Signal) counts —
// drain output does NOT refresh the orphan clock; a noisy abandoned
// process is exactly the orphan we want to reap. Without the
// watchdog, an orphan session — child still running, no client
// remembering it — pins the daemon's idle-exit because List
// reports it alive forever.
//
// Closed-and-unattached sessions get a short grace period
// (closedReapGrace) before being reaped, so quick commands like
// `bash -c 'echo result'` whose child exits before the client
// attaches don't lose their final buffered output.
type Manager struct {
	mu              sync.Mutex
	nextID          uint64
	sessions        map[uint64]*session
	idleTimeout     time.Duration
	closedReapGrace time.Duration
	stopWatchdog    chan struct{}
}

// ManagerOpts configures NewManagerWithOpts. Zero values pick
// reasonable defaults: no watchdog (operator-explicit cleanup),
// 1-minute tick when watchdog is enabled, 30-second grace period
// before reaping closed-and-unattached sessions.
type ManagerOpts struct {
	// IdleTimeout > 0 enables the watchdog. Sessions idle longer
	// than this are destroyed by the background goroutine. Idle
	// counts as "no client touch" — drain output does NOT refresh
	// the orphan clock. Zero = watchdog disabled (matches
	// NewManager's behaviour).
	IdleTimeout time.Duration

	// WatchdogTick is the watchdog's check cadence. 0 → 1 minute.
	// Tests use a much smaller value (e.g. 10 ms) to make idle
	// expiry observable in single-digit-millisecond windows.
	WatchdogTick time.Duration

	// ClosedReapGrace is the delay between a child process exiting
	// and the watchdog being allowed to reap the closed-and-
	// unattached session. Lets quick-command patterns
	// (`bash -c 'echo result'` then attach + read) capture final
	// output even when the agent's attach lands after the process
	// exits. 0 → 30 seconds (production default). Tests use much
	// smaller values to avoid 30-second waits.
	ClosedReapGrace time.Duration
}

// SpawnOpts are the create-time parameters. Either Argv or Cmd is
// required (Argv preferred — no shell-quoting hazards). When both
// are set, Argv wins.
type SpawnOpts struct {
	Argv        []string `json:"argv,omitempty"`
	Cmd         string   `json:"cmd,omitempty"`
	Env         []string `json:"env,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Cols        uint16   `json:"cols,omitempty"`
	Rows        uint16   `json:"rows,omitempty"`
	BufferBytes int      `json:"buffer_bytes,omitempty"`
}

// SessionInfo is the public view of a session — what List returns.
//
// LastClientTouch and LastDrainOutput let observers distinguish
// "client is actively driving this session" from "process is
// producing output but no client is reading." The watchdog uses
// only the former; UIs and operator dashboards can show both to
// help spot abandoned-but-noisy sessions before they hit the
// orphan timeout.
type SessionInfo struct {
	ID              uint64    `json:"id"`
	Cmd             string    `json:"cmd"`
	Alive           bool      `json:"alive"`
	Attached        bool      `json:"attached"`
	StartedAt       time.Time `json:"started_at"`
	Buffered        int       `json:"buffered"`
	Dropped         uint64    `json:"dropped"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	LastClientTouch time.Time `json:"last_client_touch"`
	LastDrainOutput time.Time `json:"last_drain_output"`
}

// AttachOpts gates the attach behaviour. Force=true steals an
// existing attach (last-attach-wins); without it, attach fails on
// already-attached sessions.
type AttachOpts struct {
	Force bool `json:"force,omitempty"`
}

// Errors surfaced to callers. The host-import layer maps these onto
// negative return codes + JSON error messages.
var (
	ErrNotFound        = errors.New("pty: session not found")
	ErrAlreadyAttached = errors.New("pty: session already attached")
	ErrNotAttached     = errors.New("pty: session not attached by caller")
	ErrClosed          = errors.New("pty: session closed")
	ErrNoCommand       = errors.New("pty: spawn requires argv or cmd")
)

type session struct {
	id        uint64
	cmd       string
	startedAt time.Time

	// proc/master are immutable once Spawn returns.
	proc   *exec.Cmd
	master *os.File

	// State guarded by mu; output ring + signaling on cond. The vt10x
	// terminal is also guarded by mu — its own internal lock would
	// race with our cond.Wait callers otherwise. Every byte written
	// to the ring is also written to vt; Snapshot reads back rendered
	// state without any wire-format concerns.
	mu       sync.Mutex
	cond     *sync.Cond
	ring     *ringBuffer
	vt       vt10x.Terminal
	cols     uint16
	rows     uint16
	dropped  uint64
	closed   bool
	exitCode *int
	attached bool

	// lastClientTouch is the wall-clock of the most recent client-driven
	// operation (Read/Write/Snapshot/Attach/Detach/Resize/Signal). The
	// watchdog uses this — and ONLY this — as the orphan signal. A
	// process producing drain output without a client touching it is
	// still an orphan: nobody's reading the output, so it shouldn't
	// pin the daemon forever. Codex + gemini caught the original
	// "drain-as-touch" design as defeating the watchdog's purpose for
	// noisy-but-abandoned processes.
	//
	// lastDrainTouch is the wall-clock of the most recent drain output.
	// Tracked separately so List/observability can show "session is
	// alive AND producing output" without conflating that with "client
	// still cares about it."
	//
	// readWaiters counts clients currently blocked inside Read awaiting
	// output. The watchdog skips sessions with waiters > 0 — a Read
	// blocked on a slow process is an active claim even though
	// lastClientTouch ages while cond.WaitTimeout sleeps.
	lastClientTouch time.Time
	lastDrainTouch  time.Time
	readWaiters     int

	// expectInProgress is set while Expect holds the session's read
	// stream. The flag is exclusivity-only — it does NOT block the
	// drain goroutine (which is the only writer) or Read/Write/Signal
	// (which would race anyway against any concurrent reader). It
	// rejects a SECOND concurrent Expect on the same session because
	// two expects competing for ring bytes would non-deterministically
	// split the input stream.
	expectInProgress bool

	// closedAt records when s.closed was set to true (drain saw EOF
	// from the master fd). The watchdog's closed-and-unattached
	// reap path uses this to enforce closedReapGrace — without it,
	// a quick command whose child exits before the client attaches
	// would lose its final buffered output to the watchdog.
	closedAt time.Time

	// version is monotonically incremented on every drain write
	// (every chunk of bytes produced by the underlying PTY's
	// child process). Snapshot consumers use it to skip the full
	// cell-grid copy when nothing's changed since their last
	// snapshot — see SnapshotVersion below + ptyblock.Model's
	// version-aware tick.
	//
	// Bumped under s.mu like everything else. Wrap-around at 2^64
	// is a non-concern: at one bump per drain chunk, even a hot
	// PTY emitting bytes at every kernel tick takes hundreds of
	// thousands of years.
	version uint64
}

// defaultClosedReapGrace is the production default for ManagerOpts.
// ClosedReapGrace. Long enough for an agent pattern like
// `shell.spawn bash -c 'echo result'` followed by `shell.attach +
// shell.read` to capture the output; short enough that genuine
// zombies don't accumulate.
const defaultClosedReapGrace = 30 * time.Second

// touch updates lastClientTouch to now. Caller must hold s.mu. This
// is the watchdog-visible signal that "a client cares about this
// session right now." Drain output uses touchDrain instead.
func (s *session) touch() {
	s.lastClientTouch = time.Now()
}

// touchDrain updates lastDrainTouch — the "process is producing
// output" signal. NOT consulted by the watchdog; surfaced via List
// for observability. Caller must hold s.mu.
func (s *session) touchDrain() {
	s.lastDrainTouch = time.Now()
}

// touchLocked updates lastClientTouch while taking the lock. Used from
// public Manager methods that need a one-shot touch outside an
// already-held lock.
func (s *session) touchLocked() {
	s.mu.Lock()
	s.lastClientTouch = time.Now()
	s.mu.Unlock()
}

// NewManager allocates an empty registry without a watchdog. Use
// NewManagerWithOpts to enable orphan-PTY cleanup.
func NewManager() *Manager {
	return &Manager{sessions: make(map[uint64]*session)}
}

// NewManagerWithOpts allocates a Manager with the supplied
// configuration. When opts.IdleTimeout > 0, starts a background
// watchdog goroutine that destroys sessions idle longer than the
// threshold. CloseAll stops the watchdog.
func NewManagerWithOpts(opts ManagerOpts) *Manager {
	grace := opts.ClosedReapGrace
	if grace <= 0 {
		grace = defaultClosedReapGrace
	}
	m := &Manager{
		sessions:        make(map[uint64]*session),
		idleTimeout:     opts.IdleTimeout,
		closedReapGrace: grace,
	}
	if opts.IdleTimeout > 0 {
		tick := opts.WatchdogTick
		if tick <= 0 {
			tick = time.Minute
		}
		m.stopWatchdog = make(chan struct{})
		go m.watchdogLoop(tick)
	}
	return m
}

// watchdogLoop runs until CloseAll. Every tick, scans sessions and
// destroys any whose lastTouched is older than idleTimeout. Snapshots
// the (id, lastTouched) pairs while holding Manager.mu, then iterates
// without it — avoids deadlock against per-session mu inside Destroy.
func (m *Manager) watchdogLoop(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-m.stopWatchdog:
			return
		case <-t.C:
			m.reapIdle()
		}
	}
}

// reapIdle destroys sessions that the watchdog considers orphans.
// Two reasons trigger destruction:
//
//  1. Closed-and-unattached past grace period: the child process
//     exited (s.closed=true), no client is attached, AND it's been
//     longer than closedReapGrace since the close. The grace exists
//     so quick commands (`bash -c 'echo result'`) whose child exits
//     before the agent attaches don't lose their final output to
//     the watchdog. Without grace, attaching one millisecond too
//     late = data lost.
//
//  2. Idle: lastClientTouch older than idleTimeout. ONLY client
//     activity counts; drain output (the process producing bytes)
//     does not refresh the orphan clock. A hung process emitting
//     periodic noise is EXACTLY the orphan we want to reap.
//
// Race-safety: the previous version had a TOCTOU window —
// candidate selection re-checked under s.mu, but Destroy was called
// outside that lock; a Write could land between the re-check and
// Destroy, getting its master fd closed mid-syscall. Closed by
// destroyIfIdle which re-validates AND tears the session down
// while atomically holding the manager-side delete with the
// per-session lock; details there.
func (m *Manager) reapIdle() {
	if m.idleTimeout <= 0 {
		return
	}
	cutoff := time.Now().Add(-m.idleTimeout)

	// Snapshot session pointers under m.mu only. Don't hold m.mu
	// while acquiring s.mu — a slow Write or Snapshot would block
	// the entire manager.
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()

	for _, s := range all {
		_ = m.destroyIfIdle(s.id, cutoff)
	}
}

// destroyIfIdle removes a session from the registry and terminates
// it ONLY if it's still idle when locked. Closes the TOCTOU that the
// previous reapIdle had: candidate-decide and destroy now happen
// inside the same s.mu acquisition, so a Write's touch under s.mu
// either lands before our Lock (we see fresh lastClientTouch and
// skip) or after our Unlock (we already removed the session and
// the Write's get(id) returns ErrNotFound — clean error path).
//
// Residual race that's worth knowing about (codex caught it on the
// third pass): a client that ALREADY held a *session pointer from
// before destroyIfIdle ran can complete its Write under s.mu, drop
// s.mu, and call s.master.Write(data) — which may succeed (n =
// len(data), err = nil) before terminate() closes the master.
// The data was delivered to the fd but the session is gone, so any
// follow-up Read returns ErrNotFound and the buffered output is
// lost. This is "false success" rather than corruption: the caller
// believes the write landed; objectively it did, but nobody can
// observe the result. Closing this fully would mean holding s.mu
// through master.Write, which would serialise every write against
// the watchdog. The session was reapable (idle 4h+ by default);
// losing a write that just barely raced is acceptable.
//
// Returns true on actual destruction. Used only by the watchdog;
// public Destroy stays unconditional (operator-explicit kills must
// always succeed).
func (m *Manager) destroyIfIdle(id uint64, cutoff time.Time) bool {
	// Acquire m.mu first so we can delete from sessions atomically
	// with the per-session check. Lock order: m.mu → s.mu (matches
	// other paths). Holding m.mu briefly is fine — destroyIfIdle is
	// not on the hot path; reapIdle calls it serially.
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	s.mu.Lock()
	shouldReap := false
	switch {
	case s.readWaiters > 0:
		// Active Read in progress; client is claiming the session
		// even though lastClientTouch may be stale while
		// cond.WaitTimeout sleeps. Don't reap.
	case s.closed && !s.attached:
		// Quick-command final-output preservation: only reap once
		// closedReapGrace has elapsed since the child exited.
		if time.Since(s.closedAt) >= m.closedReapGrace {
			shouldReap = true
		}
	case s.lastClientTouch.Before(cutoff):
		shouldReap = true
	}
	if !shouldReap {
		s.mu.Unlock()
		m.mu.Unlock()
		return false
	}
	delete(m.sessions, id)
	s.mu.Unlock()
	m.mu.Unlock()
	// Terminate outside any lock — terminate() takes s.mu itself
	// for the closed/attached writes. Once we've removed the
	// session from m.sessions, no new client calls can reach it
	// (get returns ErrNotFound), so the in-flight-client risk is
	// closed.
	s.terminate()
	return true
}

// Spawn forks a child PTY and registers it. Session starts detached.
func (m *Manager) Spawn(opts SpawnOpts) (uint64, error) {
	argv := opts.Argv
	if len(argv) == 0 {
		if opts.Cmd == "" {
			return 0, ErrNoCommand
		}
		argv = []string{"/bin/sh", "-c", opts.Cmd}
	}
	bufBytes := opts.BufferBytes
	switch {
	case bufBytes <= 0:
		bufBytes = defaultBufferBytes
	case bufBytes < minBufferBytes:
		bufBytes = minBufferBytes
	case bufBytes > maxBufferBytes:
		bufBytes = maxBufferBytes
	}

	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // by design — agent-driven.
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}

	winsize := &pty.Winsize{}
	if opts.Cols > 0 || opts.Rows > 0 {
		winsize.Cols = opts.Cols
		winsize.Rows = opts.Rows
		if winsize.Cols == 0 {
			winsize.Cols = 80
		}
		if winsize.Rows == 0 {
			winsize.Rows = 24
		}
	}

	var (
		master *os.File
		err    error
	)
	if winsize.Cols > 0 {
		master, err = pty.StartWithSize(cmd, winsize)
	} else {
		master, err = pty.Start(cmd)
	}
	if err != nil {
		return 0, fmt.Errorf("pty: start: %w", err)
	}

	cols := winsize.Cols
	rows := winsize.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	now := time.Now()
	s := &session{
		id:              atomic.AddUint64(&m.nextID, 1),
		cmd:             fmtCmd(argv),
		startedAt:       now,
		lastClientTouch: now,
		lastDrainTouch:  now,
		proc:            cmd,
		master:          master,
		ring:            newRingBuffer(bufBytes),
		vt:              vt10x.New(vt10x.WithSize(int(cols), int(rows))),
		cols:            cols,
		rows:            rows,
	}
	s.cond = sync.NewCond(&s.mu)

	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()

	go s.drain()
	go s.reap()

	return s.id, nil
}

// drain reads master into the ring forever; broadcasts on every
// chunk so blocked Read waiters wake. Each chunk is also fed to the
// vt10x emulator so Snapshot returns up-to-date rendered state — the
// emulator's Write is best-effort (any internal error is dropped, the
// ring is the canonical record).
func (s *session) drain() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.mu.Lock()
			dropped := s.ring.Write(buf[:n])
			s.dropped += dropped
			_, _ = s.vt.Write(buf[:n])
			s.version++ // bump for SnapshotVersion-aware consumers
			// Drain output is observability-only: it tells List a
			// session is producing bytes. It does NOT refresh the
			// orphan clock — a noisy-but-abandoned process should
			// still get reaped. Codex+gemini second-pass correction.
			s.touchDrain()
			s.cond.Broadcast()
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			s.closed = true
			s.closedAt = time.Now()
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
	}
}

// reap waits for the child to exit, records exit code, marks closed.
func (s *session) reap() {
	err := s.proc.Wait()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	s.mu.Lock()
	s.exitCode = &exit
	// drain goroutine will mark closed once master EOFs after the
	// child exits; race-safe to set both here.
	//
	// Broadcast so observers blocked in waitExitCode (e.g. Expect on
	// EOF) wake the moment exitCode is set rather than spinning until
	// their fallback timer fires.
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Attach claims the session for read/write. Returns ErrAlreadyAttached
// if a caller already holds the lock and Force is false.
func (m *Manager) Attach(id uint64, opts AttachOpts) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached && !opts.Force {
		return ErrAlreadyAttached
	}
	s.attached = true
	s.touch()
	return nil
}

// Detach releases the attach lock. No-op if not attached. Also
// counts as a client touch — semantically "leave this running, I
// might come back" rather than "I'm done with this." A client that
// truly wants the session reaped calls Destroy.
func (m *Manager) Detach(id uint64) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.attached = false
	s.touch()
	s.mu.Unlock()
	return nil
}

// Write sends bytes to the child's stdin. Requires attach.
func (m *Manager) Write(id uint64, data []byte) (int, error) {
	s, err := m.get(id)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if !s.attached {
		s.mu.Unlock()
		return 0, ErrNotAttached
	}
	if s.closed {
		s.mu.Unlock()
		return 0, ErrClosed
	}
	s.touch()
	s.mu.Unlock()
	return s.master.Write(data)
}

// Read drains up to maxBytes from the session's ring buffer. Blocks
// up to timeout for new bytes when the ring is empty. Returns (0, nil)
// on timeout with no data — distinct from (0, ErrClosed) when the
// session is dead and the ring is empty.
func (m *Manager) Read(id uint64, maxBytes int, timeout time.Duration) ([]byte, error) {
	s, err := m.get(id)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attached {
		return nil, ErrNotAttached
	}
	// Touch on entry; bump readWaiters so the watchdog knows a client
	// is actively blocked here even while cond.WaitTimeout sleeps with
	// s.mu released. Without the counter, a 5-minute Read on a silent
	// process would let lastClientTouch age past idleTimeout and the
	// watchdog would reap the session mid-wait.
	s.touch()
	s.readWaiters++
	defer func() { s.readWaiters--; s.touch() }()
	for s.ring.Len() == 0 {
		if s.closed {
			return nil, io.EOF
		}
		if timeout <= 0 {
			return nil, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil
		}
		s.condWaitTimeout(remaining)
	}
	out := s.ring.ReadN(maxBytes)
	return out, nil
}

// Signal sends sig to the child process. No attach required.
func (m *Manager) Signal(id uint64, sig syscall.Signal) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	if s.proc.Process == nil {
		return ErrClosed
	}
	s.touchLocked()
	return s.proc.Process.Signal(sig)
}

// Resize sets the PTY window size. No attach required. The emulator
// is resized in lockstep so Snapshot dimensions track the kernel's
// view of the tty.
func (m *Manager) Resize(id uint64, cols, rows uint16) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	if err := pty.Setsize(s.master, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return err
	}
	s.mu.Lock()
	s.vt.Resize(int(cols), int(rows))
	s.cols = cols
	s.rows = rows
	s.touch()
	s.mu.Unlock()
	return nil
}

// Destroy terminates the session: SIGTERM, grace period, SIGKILL,
// drops from registry. Idempotent.
func (m *Manager) Destroy(id uint64) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	s.terminate()
	return nil
}

func (s *session) terminate() {
	if s.proc.Process != nil {
		_ = s.proc.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			s.proc.Process.Wait() //nolint:errcheck // best-effort.
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(destroyGrace):
			_ = s.proc.Process.Kill()
		}
	}
	_ = s.master.Close()
	s.mu.Lock()
	s.closed = true
	s.attached = false
	s.cond.Broadcast()
	s.mu.Unlock()
}

// List returns a snapshot of all registered sessions.
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		exit := s.exitCode
		var exitCopy *int
		if exit != nil {
			v := *exit
			exitCopy = &v
		}
		out = append(out, SessionInfo{
			ID:              s.id,
			Cmd:             s.cmd,
			Alive:           !s.closed,
			Attached:        s.attached,
			StartedAt:       s.startedAt,
			Buffered:        s.ring.Len(),
			Dropped:         s.dropped,
			ExitCode:        exitCopy,
			LastClientTouch: s.lastClientTouch,
			LastDrainOutput: s.lastDrainTouch,
		})
		s.mu.Unlock()
	}
	m.mu.Unlock()
	return out
}

// CloseAll terminates every registered session and stops the
// idle-watchdog goroutine if running. Called from Runtime.Close —
// last-resort reaper for orphans. Idempotent: subsequent calls are
// no-ops.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	if m.stopWatchdog != nil {
		// Closing the channel signals the loop. select-case in the
		// watchdog handles already-closed by hitting the default
		// branch on next tick.
		select {
		case <-m.stopWatchdog:
			// Already closed.
		default:
			close(m.stopWatchdog)
		}
	}
	ids := make([]uint64, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Destroy(id)
	}
}

func (m *Manager) get(id uint64) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

func fmtCmd(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	out := argv[0]
	for _, a := range argv[1:] {
		out += " " + a
	}
	return out
}
