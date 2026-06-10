package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/daemon"
)

// daemonHealthState is the TUI's cached view of whether the background
// daemon is reachable. The TUI itself is daemon-independent for its turn
// loop (it attaches only at launch), so this is purely an informational
// indicator: it tells the operator whether the daemon that backs
// `stado tool run` cross-shell PTY continuity, the broker sandbox
// ceiling, and fleet/subagent work is currently up.
type daemonHealthState int

const (
	daemonHealthUnknown daemonHealthState = iota
	daemonHealthUp
	daemonHealthDown
)

// daemonProbeInterval is how often the TUI re-checks the daemon. A UDS
// handshake is cheap; 6s keeps the indicator current without noise.
const daemonProbeInterval = 6 * time.Second

type daemonProbeTickMsg struct{}

type daemonHealthMsg struct{ state daemonHealthState }

// daemonProbeTickCmd schedules the next probe tick. Self-perpetuating like
// the title-spinner chain: each tick handler returns a fresh one.
func daemonProbeTickCmd() tea.Cmd {
	return tea.Tick(daemonProbeInterval, func(time.Time) tea.Msg {
		return daemonProbeTickMsg{}
	})
}

// probeDaemonCmd dials the daemon socket with a short timeout and reports
// reachability. It does NOT auto-spawn (DialAndHandshake, not
// EnsureRunning) — the indicator must reflect reality, not create it.
func probeDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		sock, err := daemon.SocketPath()
		if err != nil || sock == "" {
			return daemonHealthMsg{state: daemonHealthUnknown}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		defer cancel()
		c, _, derr := daemon.DialAndHandshake(ctx, sock, "stado-tui-health")
		if derr != nil {
			return daemonHealthMsg{state: daemonHealthDown}
		}
		_ = c.Close()
		return daemonHealthMsg{state: daemonHealthUp}
	}
}

func onDaemonProbeTick(m *Model, _ daemonProbeTickMsg) (tea.Model, tea.Cmd) {
	// Fire the probe and reschedule the next tick.
	return m, tea.Batch(probeDaemonCmd(), daemonProbeTickCmd())
}

func onDaemonHealth(m *Model, msg daemonHealthMsg) (tea.Model, tea.Cmd) {
	m.daemonHealth = msg.state
	return m, nil
}

// daemonStatusLabel returns the template token for the status bar:
// "up" / "down" / "" (unknown — render nothing until the first probe so
// the bar doesn't flicker a misleading state at startup).
func (m *Model) daemonStatusLabel() string {
	switch m.daemonHealth {
	case daemonHealthUp:
		return "up"
	case daemonHealthDown:
		return "down"
	default:
		return ""
	}
}
