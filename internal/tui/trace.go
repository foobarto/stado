package tui

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
)

const traceLevel = slog.Level(-8)

func tuiTraceEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("STADO_TUI_TRACE")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func tuiTrace(msg string, args ...any) {
	if !tuiTraceEnabled() {
		return
	}
	slog.Log(context.Background(), traceLevel, msg, args...)
}

func tuiTraceCall(name string, args ...any) func(...any) {
	if !tuiTraceEnabled() {
		return func(...any) {}
	}
	start := time.Now()
	slog.Log(context.Background(), traceLevel, "enter "+name, args...)
	return func(extra ...any) {
		all := make([]any, 0, len(extra)+2)
		all = append(all, "duration_ms", time.Since(start).Milliseconds())
		all = append(all, extra...)
		slog.Log(context.Background(), traceLevel, "exit "+name, all...)
	}
}
