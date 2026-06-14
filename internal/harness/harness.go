// Package harness provides the security-research harness system-prompt
// addition (EP-0030). It lives in internal/ so every agent-launching surface —
// `stado run`, the TUI, the ACP server, and the headless server — can inject
// it identically when [harness].mode = "security" (or --mode security). Before
// this package existed the loader lived in package main and only `stado run`
// applied it, so the config knob was silently ignored on the other surfaces.
package harness

import (
	"os"
	"path/filepath"
	"strings"
)

// SecurityBuiltin is the default security-research system-prompt addition.
// Operators override it by creating .stado/harness/security.md in their project.
const SecurityBuiltin = `# Security-research mode

You are operating in security-research mode. The following discipline applies
to ALL work in this session, regardless of what the user asks.

## Recon first

Never move to exploitation before mapping the attack surface. Phase order:
1. Enumerate what exists (services, endpoints, parameters, versions, assets).
2. Understand what each component does and where inputs flow.
3. Hypothesise weaknesses. THEN test.

Skipping recon is the primary source of missed findings and wasted effort.

## Abusability filter — PoC or it didn't happen

"Vulnerable package present" / "config smell" / "theoretical bypass" are NOT
findings without an end-to-end attacker PoC demonstrating observable harm.
Maintain separate lists: **candidate** (unverified) vs **verified** (PoC done).
Never promote a candidate to a finding without running the PoC.

## Prerequisite vs impact check

Before reporting a finding, identify the access level required to trigger it.
If an attacker at that access already has the claimed capability via other means,
you are restating the prerequisite, not adding uplift. Compute the DELTA:
stealth, detachment, persistence-after-remediation, time-window extension,
cross-tenant impact, infrastructure reduction.

## Anti-confirmation bias

- First-look signals are HYPOTHESES, not findings. Walk back claims that do not
  survive scrutiny.
- When stuck on one approach for more than 30 minutes with no progress, switch
  layer (app → transport → infrastructure) or switch actor (developer → SRE → auditor).
- Never speculate aloud. "Probably works because..." without a test is noise.

## Data organisation

Maintain findings in notes/engagements/<target>/:
  recon/        — scan output, enumeration notes
  loot/         — captured secrets, credentials, session tokens
  writeup.md    — structured finding narrative
  scratch.md    — working notes, not for sharing

## Programming discipline

Helper scripts written during an engagement are throwaway tools, not production
code. No over-engineering. Minimum viable for the task at hand.`

// maxHarnessBytes caps the project harness override (1 MiB; the builtin is a
// few KB).
const maxHarnessBytes int64 = 1 << 20

// LoadSecurity returns the security harness system-prompt addition: the
// project's .stado/harness/security.md override if present, else SecurityBuiltin.
func LoadSecurity(workdir string) string {
	if workdir != "" {
		custom := filepath.Join(workdir, ".stado", "harness", "security.md")
		// Reject symlinks (Lstat + IsRegular) and oversize files: this path is
		// repo-controlled, and a bare ReadFile would follow a symlink to an
		// operator credential file and splice its contents into the system
		// prompt (exfil).
		if info, err := os.Lstat(custom); err == nil && info.Mode().IsRegular() && info.Size() <= maxHarnessBytes {
			if data, err := os.ReadFile(custom); err == nil {
				content := strings.TrimSpace(string(data))
				if content != "" {
					return content
				}
			}
		}
	}
	return SecurityBuiltin
}

// Prepend returns base with the security harness prepended when mode is
// "security"; otherwise base unchanged. Centralises the EP-0030 injection so
// every agent surface applies it identically.
func Prepend(base, workdir, mode string) string {
	if mode != "security" {
		return base
	}
	add := LoadSecurity(workdir)
	if add == "" {
		return base
	}
	if base == "" {
		return add
	}
	return add + "\n\n---\n\n" + base
}
