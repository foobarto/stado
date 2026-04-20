package main

// Minimal ANSI colour helpers for CLI output that hits a human at
// a terminal. Respects:
//   - NO_COLOR env var (see https://no-color.org)
//   - FORCE_COLOR=1 (common override for CI where a pty may not
//     register as one but the user still wants colour)
//   - isatty check on the output stream
//
// The stado TUI has lipgloss for richer rendering; the CLI
// subcommands are simpler and don't need the render pipeline. This
// file is that minimal seam.

import (
	"os"

	"github.com/mattn/go-isatty"
)

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiGreen = "\x1b[32m"
	ansiGrey  = "\x1b[90m"
)

// useColor returns true when ANSI colour is appropriate for the
// given file. Applies the full NO_COLOR / FORCE_COLOR / isatty
// cascade so output stays plain when piped or captured.
func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return isatty.IsTerminal(f.Fd())
}

// colorizeStatus wraps the status cell's padded string in the
// appropriate ANSI colour. The caller passes both the raw status
// word (for the branch) and the already-padded cell (so the pad
// survives ANSI noise). Kept separate so tests can verify the
// branching without poking at escape codes.
func colorizeStatus(status, padded string) string {
	switch status {
	case "live":
		return ansiGreen + padded + ansiReset
	case "idle":
		// Idle is muted — the session's resting, nothing happening.
		return ansiGrey + padded + ansiReset
	case "detached":
		return ansiDim + padded + ansiReset
	}
	return padded
}
