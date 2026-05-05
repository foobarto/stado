package tui

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// monitorState tracks an active /monitor process. EP-0036.
type monitorState struct {
	cmd    string
	cancel context.CancelFunc
}

// monitorDoneMsg fires when the monitored process exits.
type monitorDoneMsg struct{ err error }

// handleMonitorCmd processes a /monitor slash command. EP-0036.
//
//   /monitor <cmd>   start streaming <cmd> stdout as system blocks
//   /monitor stop    kill the background process
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
	m.monitor = &monitorState{cmd: rest, cancel: cancel}
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("monitor started: %s", rest)})

	return m.startMonitorCmd(ctx, rest)
}

// startMonitorCmd returns a tea.Cmd that runs the shell command and
// streams each stdout line as a monitorLineMsg.
func (m *Model) startMonitorCmd(ctx context.Context, shellCmd string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd) //nolint:gosec
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return monitorDoneMsg{err: err}
		}
		if err := cmd.Start(); err != nil {
			return monitorDoneMsg{err: err}
		}

		// Stream lines back to the TUI via individual tea.Msg returns.
		// We can't return multiple messages from one Cmd, so we use a
		// recursive approach: each line returns itself, then the next
		// Cmd reads the following line.
		scanner := bufio.NewScanner(stdout)
		lines := make([]string, 0, 32)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		_ = cmd.Wait()
		if len(lines) == 0 {
			return monitorDoneMsg{}
		}
		// Return the first line as a message; remaining lines will be
		// delivered via subsequent batched messages.
		return monitorLinesMsg(lines)
	}
}

// monitorLinesMsg delivers a batch of monitor output lines.
type monitorLinesMsg []string
