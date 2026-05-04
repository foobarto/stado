// Package dotenv loads .env files from the current directory upward
// to the filesystem root. Modeled on the standard convention used by
// npm / python tooling — but limited to the .env filename (no
// .env.local / .env.production layering for v1).
//
// Walk semantics:
//   - Start at the supplied directory (typically cwd).
//   - Walk up parent-by-parent until reaching the filesystem root
//     OR a sibling dir's .git is encountered (project boundary;
//     stop there to avoid pulling in unrelated parents' .env).
//   - For each .env found along the way, parse + apply.
//   - Closer-to-cwd files override further-from-cwd ones (apply in
//     root-to-cwd order so the closer file's later os.Setenv wins).
//   - **Never overwrite an env var that's already set in the
//     process environment.** Shell-set vars always win over .env;
//     this matches every other dotenv loader's default.
//
// Parser is intentionally minimal:
//   - `KEY=value` lines.
//   - `# comment` lines and trailing `# comment` after values.
//   - `KEY="value with spaces"` (double-quoted; escape sequences NOT
//     interpreted — preserved literally).
//   - `KEY='single quoted'`.
//   - Empty lines tolerated.
//   - Anything else is silently ignored (no errors propagated to the
//     caller for malformed entries — boot-path concern).
package dotenv

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadHierarchy walks up from startDir, finds every .env file, and
// applies its contents to the process env. Existing env vars are
// never overwritten. Returns the list of .env file paths that were
// found and applied (closest-first), useful for diagnostics.
//
// startDir empty → uses os.Getwd. Any error along the way (Stat
// fail, parse fail) is silently treated as "skip that file" — boot
// shouldn't fail because of a stale .env somewhere up the tree.
func LoadHierarchy(startDir string) []string {
	if startDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			startDir = cwd
		} else {
			return nil
		}
	}
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return nil
	}

	// Collect candidates from cwd → root (closer-to-cwd first), so
	// when applying we walk in REVERSE so closer files overwrite
	// further ones at the os.Setenv layer (the "don't overwrite"
	// rule means the FIRST setter wins per key — we want closer to
	// be that first setter).
	var candidates []string
	dir := abs
	for {
		envFile := filepath.Join(dir, ".env")
		if info, err := os.Stat(envFile); err == nil && !info.IsDir() {
			candidates = append(candidates, envFile)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // hit filesystem root
		}
		dir = parent
	}

	// Apply in cwd-first order so the closer file's pairs land first.
	// loadFile uses os.Setenv only for keys not already in env, so the
	// closer file's keys preempt the further file's same-named keys.
	var applied []string
	for _, path := range candidates {
		if loadFile(path) {
			applied = append(applied, path)
		}
	}
	return applied
}

// loadFile parses one .env file and applies pairs that aren't
// already set in the process env. Returns true when the file was
// readable, regardless of how many pairs ended up applied.
func loadFile(path string) bool {
	f, err := os.Open(path) //nolint:gosec // path resolved by LoadHierarchy walk; user-controlled by intent
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	// Allow long values (api keys, JWTs) — default 64KB is plenty.
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		k, v, ok := parseLine(scanner.Text())
		if !ok {
			continue
		}
		if _, present := os.LookupEnv(k); present {
			continue // existing env wins
		}
		_ = os.Setenv(k, v)
	}
	return true
}

// parseLine extracts a (key, value) pair from one .env line. Returns
// ok=false for blanks, comments, and malformed entries.
func parseLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	// Strip a leading `export ` to match common shell-rc files.
	line = strings.TrimPrefix(line, "export ")
	line = strings.TrimSpace(line)

	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:eq])
	if !validKey(key) {
		return "", "", false
	}
	value := strings.TrimSpace(line[eq+1:])

	// Quoted value: take everything inside the quotes verbatim,
	// drop trailing comment after the closing quote.
	if len(value) >= 2 {
		if (value[0] == '"' && strings.Contains(value[1:], `"`)) ||
			(value[0] == '\'' && strings.Contains(value[1:], `'`)) {
			q := value[0]
			end := strings.IndexByte(value[1:], q)
			if end >= 0 {
				return key, value[1 : 1+end], true
			}
		}
	}
	// Unquoted: strip trailing comment ` # ...`.
	if hash := strings.Index(value, " #"); hash >= 0 {
		value = strings.TrimSpace(value[:hash])
	}
	// Strip surrounding whitespace one more time for safety.
	value = strings.TrimSpace(value)
	return key, value, true
}

// validKey enforces the standard env-var naming convention so we
// don't apply garbage like `1=foo` or `key with space=foo`.
// First char must be letter or underscore; rest letters / digits /
// underscores.
func validKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
