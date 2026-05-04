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
)

const (
	defaultBufferBytes = 64 * 1024
	minBufferBytes     = 4 * 1024
	maxBufferBytes     = 4 * 1024 * 1024
	destroyGrace       = 2 * time.Second
)

// Manager is a thread-safe registry of live PTY sessions.
type Manager struct {
	mu       sync.Mutex
	nextID   uint64
	sessions map[uint64]*session
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
type SessionInfo struct {
	ID        uint64    `json:"id"`
	Cmd       string    `json:"cmd"`
	Alive     bool      `json:"alive"`
	Attached  bool      `json:"attached"`
	StartedAt time.Time `json:"started_at"`
	Buffered  int       `json:"buffered"`
	Dropped   uint64    `json:"dropped"`
	ExitCode  *int      `json:"exit_code,omitempty"`
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
	ErrNotFound       = errors.New("pty: session not found")
	ErrAlreadyAttached = errors.New("pty: session already attached")
	ErrNotAttached    = errors.New("pty: session not attached by caller")
	ErrClosed         = errors.New("pty: session closed")
	ErrNoCommand      = errors.New("pty: spawn requires argv or cmd")
)

type session struct {
	id        uint64
	cmd       string
	startedAt time.Time

	// proc/master are immutable once Spawn returns.
	proc   *exec.Cmd
	master *os.File

	// State guarded by mu; output ring + signaling on cond.
	mu       sync.Mutex
	cond     *sync.Cond
	ring     *ringBuffer
	dropped  uint64
	closed   bool
	exitCode *int
	attached bool
}

// NewManager allocates an empty registry.
func NewManager() *Manager {
	return &Manager{sessions: make(map[uint64]*session)}
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

	s := &session{
		id:        atomic.AddUint64(&m.nextID, 1),
		cmd:       fmtCmd(argv),
		startedAt: time.Now(),
		proc:      cmd,
		master:    master,
		ring:      newRingBuffer(bufBytes),
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
// chunk so blocked Read waiters wake.
func (s *session) drain() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.mu.Lock()
			dropped := s.ring.Write(buf[:n])
			s.dropped += dropped
			s.cond.Broadcast()
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			s.closed = true
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
	return nil
}

// Detach releases the attach lock. No-op if not attached.
func (m *Manager) Detach(id uint64) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.attached = false
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
	return s.proc.Process.Signal(sig)
}

// Resize sets the PTY window size. No attach required.
func (m *Manager) Resize(id uint64, cols, rows uint16) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	return pty.Setsize(s.master, &pty.Winsize{Cols: cols, Rows: rows})
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
			ID:        s.id,
			Cmd:       s.cmd,
			Alive:     !s.closed,
			Attached:  s.attached,
			StartedAt: s.startedAt,
			Buffered:  s.ring.Len(),
			Dropped:   s.dropped,
			ExitCode:  exitCopy,
		})
		s.mu.Unlock()
	}
	m.mu.Unlock()
	return out
}

// CloseAll terminates every registered session. Called from
// Runtime.Close — last-resort reaper for orphans.
func (m *Manager) CloseAll() {
	m.mu.Lock()
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
