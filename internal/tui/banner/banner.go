// Package banner serves the startup banner shown in the TUI's empty
// chat area before the user sends their first message. Two assets
// are embedded at compile time:
//
//   - banner.ansi: chafa-generated 256-color block art (the preferred
//                  form when the terminal supports colour)
//   - banner.txt:  plain unicode-block render (5-level ramp, no
//                  escape sequences) used when colour is disabled
//                  (NO_COLOR env / non-tty output / 16-color terms)
//
// Resolution honours the cross-vendor `NO_COLOR` convention: any
// non-empty value returns the plain variant. FORCE_COLOR is ignored
// because the plain banner is a strict subset of what the colour one
// achieves — there's no upside to forcing 256-colour rendering in a
// 16-colour terminal (the palette doesn't match and the result
// looks worse than the plain block art).
package banner

import (
	_ "embed"
	"os"
)

//go:embed banner.ansi
var ansiBanner string

//go:embed banner.txt
var plainBanner string

// String returns the banner appropriate for the current environment:
// plain when NO_COLOR is set, colour otherwise. Callers render it
// verbatim — no further processing.
func String() string {
	if os.Getenv("NO_COLOR") != "" {
		return plainBanner
	}
	return ansiBanner
}

// Plain is the no-escape variant. Useful for `stado banner --plain`
// or for tests that don't want to assert against ANSI byte shape.
func Plain() string { return plainBanner }

// ANSI is the coloured variant — exposed so callers that know the
// terminal supports 256 colours can force it regardless of env.
func ANSI() string { return ansiBanner }
