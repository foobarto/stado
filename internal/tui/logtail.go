package tui

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

const maxSidebarLogLines = 5

type logTailWriter struct {
	model   *Model
	mu      sync.Mutex
	partial string
}

func newTUILogger(m *Model) *slog.Logger {
	level := slog.Level(slog.LevelInfo)
	if tuiTraceEnabled() {
		level = traceLevel
	}
	return slog.New(slog.NewTextHandler(&logTailWriter{model: m}, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key != slog.LevelKey {
				return a
			}
			if lvl, ok := a.Value.Any().(slog.Level); ok && lvl == traceLevel {
				a.Value = slog.StringValue("TRACE")
			}
			return a
		},
	}))
}

func (w *logTailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	buf := w.partial + string(p)
	lines := strings.Split(buf, "\n")
	w.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		line = normalizeSidebarLog(line)
		if line == "" || w.model == nil {
			continue
		}
		w.model.pushLogLine(line)
	}
	return len(p), nil
}

func normalizeSidebarLog(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	level := ""
	if idx := strings.Index(line, "level="); idx >= 0 {
		rest := line[idx+len("level="):]
		level = rest
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			level = rest[:sp]
		}
	}
	if idx := strings.Index(line, "msg="); idx >= 0 {
		rest := strings.TrimSpace(strings.TrimPrefix(line[idx:], "msg="))
		msg := rest
		if strings.HasPrefix(rest, `"`) {
			if end := strings.Index(rest[1:], `"`); end >= 0 {
				msg = rest[1 : end+1]
				rest = strings.TrimSpace(rest[end+2:])
			} else {
				msg = strings.Trim(rest, `"`)
				rest = ""
			}
		} else if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			msg = rest[:sp]
			rest = strings.TrimSpace(rest[sp+1:])
		} else {
			rest = ""
		}
		if rest != "" {
			msg += " " + rest
		}
		var prefix []string
		if ts := extractSidebarLogTime(line); ts != "" {
			prefix = append(prefix, ts)
		}
		if level != "" {
			prefix = append(prefix, level)
		}
		if len(prefix) > 0 {
			return strings.TrimSpace(strings.Join(prefix, " ") + " " + msg)
		}
		return msg
	}
	parts := strings.Fields(line)
	if len(parts) >= 3 && strings.Count(parts[0], "/") == 2 && strings.Count(parts[1], ":") >= 2 {
		line = strings.Join(parts[2:], " ")
	}
	return strings.TrimSpace(line)
}

func extractSidebarLogTime(line string) string {
	idx := strings.Index(line, "time=")
	if idx < 0 {
		return ""
	}
	rest := line[idx+len("time="):]
	raw := rest
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		raw = rest[:sp]
	}
	raw = strings.Trim(raw, `"`)
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return ""
	}
	return ts.Format("15:04:05.000")
}

func (m *Model) pushLogLine(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if !m.recordLogLine(line) {
		return
	}
	if m.program != nil {
		go m.sendMsg(logTailMsg{line: line})
	}
}

func (m *Model) recordLogLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	if n := len(m.logTail); n > 0 && m.logTail[n-1] == line {
		return false
	}
	m.logTail = append(m.logTail, line)
	if len(m.logTail) > maxSidebarLogLines {
		m.logTail = append([]string(nil), m.logTail[len(m.logTail)-maxSidebarLogLines:]...)
	}
	return true
}

func (m *Model) sidebarLogLines() []sidebarLine {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	if len(m.logTail) == 0 {
		return nil
	}
	if !m.sidebarDebug && m.state != stateError && !logTailHasWarning(m.logTail) && !logTailHasProgress(m.logTail) {
		return nil
	}
	out := make([]sidebarLine, 0, len(m.logTail))
	for _, line := range m.logTail {
		tone := "muted"
		switch {
		case strings.HasPrefix(line, "ERROR ") || strings.Contains(line, " ERROR "):
			tone = "error"
		case strings.HasPrefix(line, "WARN ") || strings.Contains(line, " WARN "):
			tone = "warning"
		case strings.HasPrefix(line, "TRACE ") || strings.Contains(line, " TRACE "):
			tone = "accent"
		case strings.HasPrefix(line, "INFO ") || strings.Contains(line, " INFO "):
			tone = "text_dim"
		case strings.HasPrefix(line, "PROGRESS ") || strings.Contains(line, " PROGRESS "):
			tone = "accent"
		}
		out = append(out, sidebarLine{Text: line, Tone: tone})
	}
	return out
}

func logTailHasWarning(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "ERROR ") || strings.Contains(line, " ERROR ") ||
			strings.HasPrefix(line, "WARN ") || strings.Contains(line, " WARN ") {
			return true
		}
	}
	return false
}

// logTailHasProgress reports whether the tail contains a stado_progress
// emission. Progress lines surface in the sidebar regardless of
// --sidebar-debug because the plugin author chose to emit them. EP-0038h.
func logTailHasProgress(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "PROGRESS ") || strings.Contains(line, " PROGRESS ") {
			return true
		}
	}
	return false
}
