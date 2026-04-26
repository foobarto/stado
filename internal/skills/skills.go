// Package skills discovers project-level skill files and exposes them
// as reusable prompts. A "skill" is a markdown file with YAML-style
// frontmatter that names and describes it:
//
//	---
//	name: refactor
//	description: Extract a function from the current selection
//	---
//	Find repeated code near the cursor and factor it out into a
//	helper. Prefer the narrowest shared scope; keep call sites
//	unchanged.
//
// Stado loads every `.stado/skills/*.md` file under the cwd walk at
// TUI startup and registers each as a slash command `/skill:<name>`.
// Invocation seeds the body into the conversation as a user message
// so the LLM acts on it. Matches the Claude Code "skills" convention
// that's becoming a de-facto standard across coding agents.
//
// Scope limitations (deliberate, by design):
//   - Plain text body, no includes / templating / args. A skill is a
//     one-shot prompt, not a macro system — keep it skimmable.
//   - Only cwd + direct parents are scanned; no user-global
//     ~/.config/stado/skills/ yet. The project-root scope matches
//     AGENTS.md's semantics and avoids the "why did this skill run?"
//     problem of global state.
package skills

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
)

// Skill is one parsed skill file.
type Skill struct {
	Name        string // from frontmatter; falls back to filename stem
	Description string // from frontmatter; may be empty
	Body        string // everything after the frontmatter
	Path        string // absolute path on disk (for error messages)
}

const (
	maxSkillFileBytes  int64 = 1 << 20
	maxSkillDirEntries       = 4096
	skillReadDirBatch        = 128
)

// Load walks from `start` upward and gathers every `.stado/skills/*.md`
// it finds. Nearest-wins when two levels define a skill with the same
// name — a module-local skill.md overrides a repo-root one, same as
// the instructions loader's policy.
//
// A clean miss (no `.stado/skills/` anywhere up the tree) returns a
// nil slice and nil error. Unreadable skill files produce a per-file
// error returned alongside any successfully-loaded skills so a single
// broken skill doesn't black-hole the rest.
func Load(start string) ([]Skill, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("skills: abs %s: %w", start, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("skills: resolve %s: %w", start, err)
	}

	// Walk cwd → parents, collecting skill directories bottom-up.
	// Bottom-up order means later overrides earlier; we invert that
	// by keeping the FIRST occurrence of each name (nearest wins).
	var dirs []string
	dir := abs
	for {
		candidate := filepath.Join(dir, ".stado", "skills")
		if info, statErr := os.Lstat(candidate); statErr == nil && info.Mode().IsDir() && info.Mode()&os.ModeSymlink == 0 {
			dirs = append(dirs, candidate)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("skills: lstat %s: %w", candidate, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	seen := map[string]bool{}
	var out []Skill
	var firstErr error
	for _, d := range dirs {
		loaded, loadErr := loadSkillDir(d, seen)
		if loadErr != nil {
			if firstErr == nil {
				firstErr = loadErr
			}
		}
		out = append(out, loaded...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, firstErr
}

func loadSkillDir(d string, seen map[string]bool) ([]Skill, error) {
	root, err := workdirpath.OpenRootNoSymlink(d)
	if err != nil {
		return nil, fmt.Errorf("skills: open dir %s: %w", d, err)
	}
	defer func() { _ = root.Close() }()

	entries, err := readSkillDirEntries(root, maxSkillDirEntries)
	if err != nil {
		return nil, fmt.Errorf("skills: read dir %s: %w", d, err)
	}

	var out []Skill
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(d, name)
		info, statErr := root.Lstat(name)
		if statErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("skills: lstat %s: %w", path, statErr)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		body, readErr := workdirpath.ReadRootRegularFileLimited(root, name, maxSkillFileBytes)
		if readErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("skills: read %s: %w", path, readErr)
			}
			continue
		}
		sk := parse(string(body))
		sk.Path = path
		if sk.Name == "" {
			// Fall back to the filename stem.
			sk.Name = strings.TrimSuffix(name, ".md")
		}
		if seen[sk.Name] {
			continue // nearer dir already claimed this name
		}
		seen[sk.Name] = true
		out = append(out, sk)
	}
	return out, firstErr
}

func readSkillDirEntries(root *os.Root, maxEntries int) ([]os.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()

	var out []os.DirEntry
	entriesSeen := 0
	for {
		entries, readErr := dir.ReadDir(skillReadDirBatch)
		for _, e := range entries {
			entriesSeen++
			if entriesSeen > maxEntries {
				return nil, fmt.Errorf("skill directory contains more than %d entries", maxEntries)
			}
			name := e.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("invalid skill directory entry name %q", name)
			}
			out = append(out, e)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// parse strips the optional `---`-bounded YAML frontmatter off a skill
// body and pulls out name/description. The parser is deliberately
// minimal — one `key: value` per line, no quoting, no nested objects.
// Anything more elaborate is outside the "one-shot prompt" scope.
func parse(src string) Skill {
	sk := Skill{}
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		sk.Body = src
		return sk
	}
	// Walk until the closing `---`.
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		// Unterminated frontmatter — treat the whole thing as body.
		sk.Body = src
		return sk
	}
	for _, ln := range lines[1:end] {
		k, v, ok := splitKV(ln)
		if !ok {
			continue
		}
		switch k {
		case "name":
			sk.Name = v
		case "description":
			sk.Description = v
		}
	}
	sk.Body = strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
	return sk
}

// splitKV turns "name: refactor" into ("name", "refactor", true).
// Returns (_, _, false) for blank, commented, or malformed lines.
func splitKV(ln string) (string, string, bool) {
	ln = strings.TrimSpace(ln)
	if ln == "" || strings.HasPrefix(ln, "#") {
		return "", "", false
	}
	colon := strings.IndexByte(ln, ':')
	if colon <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(ln[:colon])
	val := strings.TrimSpace(ln[colon+1:])
	return key, val, true
}
