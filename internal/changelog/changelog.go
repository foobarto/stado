// Package changelog exposes the most recent CHANGELOG section to the binary
// (e.g. the TUI landing screen). latest.md is extracted from the repo-root
// CHANGELOG.md by `make` and committed, so `go build` works without the
// generate step; a drift-guard test keeps the copy honest.
package changelog

import (
	_ "embed"
	"strings"
)

//go:embed latest.md
var latest string

// Release is the parsed most-recent CHANGELOG entry.
type Release struct {
	Version    string   // e.g. "v0.61.0"
	Title      string   // the headline after the version
	Highlights []string // bold lead-ins of the first few bullets
}

// Latest parses the embedded most-recent release section. Returns a zero
// Release if the section can't be parsed (the caller renders nothing).
func Latest() Release {
	var r Release
	for _, line := range strings.Split(latest, "\n") {
		switch {
		case r.Version == "" && strings.HasPrefix(line, "## v"):
			// "## v0.61.0 — Title — 2026-06-11"
			parts := strings.SplitN(strings.TrimPrefix(line, "## "), " — ", 3)
			r.Version = strings.TrimSpace(parts[0])
			if len(parts) >= 2 {
				r.Title = strings.TrimSpace(parts[1])
			}
		case len(r.Highlights) < 4 && strings.HasPrefix(strings.TrimSpace(line), "- **"):
			// "- **Lead-in.** rest..." → "Lead-in"
			t := strings.TrimPrefix(strings.TrimSpace(line), "- **")
			if i := strings.Index(t, "**"); i >= 0 {
				if lead := strings.TrimRight(strings.TrimSpace(t[:i]), "."); lead != "" {
					r.Highlights = append(r.Highlights, lead)
				}
			}
		}
	}
	return r
}
