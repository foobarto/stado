package tui

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// monitorState tracks an active /monitor process. EP-0036.
type monitorState struct {
	cmd    string
	cancel context.CancelFunc
	gen    uint64 // identifies this monitor instance; see Model.monitorGen
}

// monitorDoneMsg fires when the monitored process exits. gen identifies the
// monitor instance so a stale completion from a superseded monitor is ignored.
type monitorDoneMsg struct {
	gen uint64
	err error
}

// handleMonitorCmd processes a /monitor slash command. EP-0036.
//
//	/monitor <cmd>   start streaming <cmd> stdout as system blocks
//	/monitor stop    kill the background process
func (m *Model) handleMonitorCmd(rest string) tea.Cmd {
	rest = strings.TrimSpace(rest)

	if rest == "stop" || rest == "off" {
		if m.monitor == nil {
			m.appendBlock(block{kind: "system", body: "no active monitor"})
			return nil
		}
		m.monitor.cancel()
		m.monitor = nil
		m.appendBlock(block{kind: "system", body: "monitor stopped"})
		return nil
	}
	if rest == "" {
		m.appendBlock(block{kind: "system", body: "usage: /monitor <cmd>  or  /monitor stop"})
		return nil
	}
	if m.monitor != nil {
		m.appendBlock(block{kind: "system", body: "monitor already running — run /monitor stop first"})
		return nil
	}

	ctx, cancel := context.WithCancel(m.rootCtx)
	m.monitorGen++
	gen := m.monitorGen
	m.monitor = &monitorState{cmd: rest, cancel: cancel, gen: gen}
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("monitor started: %s", rest)})

	return m.startMonitorCmd(ctx, rest, gen)
}

// startMonitorCmd returns a tea.Cmd that spawns the monitor goroutine.
// The Cmd itself returns immediately (the goroutine owns the lifetime);
// each stdout line is delivered live via m.sendMsg as a monitorLineMsg,
// and a monitorDoneMsg is sent when the process exits. EP-0036 requires
// per-line streaming so long-running monitors (tail -f, ping) surface
// output as it arrives rather than only after the process exits.
func (m *Model) startMonitorCmd(ctx context.Context, shellCmd string, gen uint64) tea.Cmd {
	return func() tea.Msg {
		go streamMonitor(ctx, m.sendMsg, shellCmd, gen)
		return nil
	}
}

// streamMonitor runs shellCmd and pushes each stdout line to send as a
// monitorLineMsg, in real time, then a terminal monitorDoneMsg. It blocks
// until the process exits or ctx is cancelled; callers run it in a
// goroutine. Extracted from startMonitorCmd so it can be exercised with a
// capturing send in tests without a live *tea.Program.
func streamMonitor(ctx context.Context, send func(tea.Msg), shellCmd string, gen uint64) {
	cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd) //nolint:gosec
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		send(monitorDoneMsg{gen: gen, err: err})
		return
	}
	if err := cmd.Start(); err != nil {
		send(monitorDoneMsg{gen: gen, err: err})
		return
	}

	// Deliver each line the moment the scanner reads it, so the session
	// sees streamed output instead of one batch on exit.
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		send(monitorLineMsg{gen: gen, line: scanner.Text()})
	}
	err = cmd.Wait()
	// A ctx-cancelled process (/monitor stop) reports a non-nil Wait
	// error; that's an expected, user-initiated stop, not a failure.
	if ctx.Err() != nil {
		err = nil
	}
	send(monitorDoneMsg{gen: gen, err: err})
}

// monitorLineMsg delivers a single monitor output line. gen identifies the
// monitor instance so a stale line from a superseded monitor is ignored.
type monitorLineMsg struct {
	gen  uint64
	line string
}
