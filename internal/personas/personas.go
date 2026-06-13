// Package personas resolves persona files — yaml-frontmatter + markdown
// bodies that act as the agent's operating manual. Layers per
// EP/personas: cwd > user > bundled, one-level inheritance via
// `inherits:` frontmatter, deduped by name.
package personas

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// library holds the bundled persona files. Their filenames are the
// canonical names (without .md). The default persona is read from
// `default.md` here.
//
//go:embed library/*.md
var library embed.FS

// Persona is one resolved persona. Body is the assembled markdown
// after inheritance has been merged (parent body → child body).
type Persona struct {
	Name             string   `yaml:"name"`
	Title            string   `yaml:"title"`
	Description      string   `yaml:"description"`
	Inherits         string   `yaml:"inherits,omitempty"`
	Collaborators    []string `yaml:"collaborators,omitempty"`
	RecommendedTools []string `yaml:"recommended_tools,omitempty"`
	Version          int      `yaml:"version,omitempty"`

	// Per-persona additive scope (2026-06-13). All three layer ON TOP of
	// the global defaults when the persona is active — personas EXTEND, they
	// never hide/restrict the global surface.
	//
	//	Tools   — tool names/globs promoted into the per-turn autoload surface.
	//	          Unioned with RecommendedTools via EffectiveTools().
	//	Skills  — skill-file paths resolved relative to filepath.Dir(SourcePath);
	//	          loaded additively on top of the cwd-discovered skill set.
	//	Plugins — background-plugin ids added to the launch-time set (launch-only;
	//	          a live /persona switch does NOT start/stop background plugins).
	Tools   []string `yaml:"tools,omitempty"`
	Skills  []string `yaml:"skills,omitempty"`
	Plugins []string `yaml:"plugins,omitempty"`

	// Body is the markdown content (no frontmatter). For an inheriting
	// persona this is the parent body followed by this body.
	Body string `yaml:"-"`

	// SourcePath records where the persona was loaded from. Empty for
	// bundled. Used for debugging + the `/persona view` UI.
	SourcePath string `yaml:"-"`
}

const personasSubdir = "personas"

// Resolver locates persona files. The empty zero-value uses the
// process's HOME and falls back to bundled-only when no project /
// user paths are set. Pass cwd / configDir explicitly so tests can
// inject temp dirs.
type Resolver struct {
	// CWD is the operator's working directory. Personas under
	// {CWD}/.stado/personas/ shadow user + bundled.
	CWD string
	// ConfigDir is the user-config base (~/.stado/ or
	// $XDG_CONFIG_HOME/stado/). Personas under <ConfigDir>/personas/
	// shadow bundled.
	ConfigDir string
}

// Load resolves a persona by name. Project beats user beats bundled.
// Returns ErrNotFound when no source has the name.
func (r Resolver) Load(name string) (*Persona, error) {
	visited := map[string]bool{}
	return r.loadResolved(name, visited)
}

// ErrNotFound signals a persona name doesn't resolve in any source. The
// message is the bare condition ("not found"); callers that know the name
// wrap it (see readSource) so it reads "persona %q: not found" without the
// doubled "persona: persona:" prefix the old "persona: not found" produced.
var ErrNotFound = errors.New("not found")

// ErrInheritanceCycle is returned when `inherits:` chains form a loop.
var ErrInheritanceCycle = errors.New("persona: inheritance cycle")

func (r Resolver) loadResolved(name string, visited map[string]bool) (*Persona, error) {
	if visited[name] {
		return nil, fmt.Errorf("%w: %s", ErrInheritanceCycle, name)
	}
	visited[name] = true

	raw, src, err := r.readSource(name)
	if err != nil {
		return nil, err
	}
	p, body, err := parsePersona(raw)
	if err != nil {
		return nil, fmt.Errorf("persona %s: %w", name, err)
	}
	p.Name = name
	p.SourcePath = src

	if p.Inherits != "" {
		base, err := r.loadResolved(p.Inherits, visited)
		if err != nil {
			return nil, fmt.Errorf("persona %s inherits %s: %w", name, p.Inherits, err)
		}
		// Parent body first, then a separator, then this body.
		p.Body = base.Body + "\n\n---\n\n" + body
		// Additive scope inheritance (2026-06-13): a child's effective
		// tools/skills/plugins are union(parent, child) — parent entries
		// first, child's appended, deduped. Mirrors the body merge so the
		// chain accumulates scope on the way down.
		p.Tools = unionStrings(base.Tools, p.Tools)
		p.RecommendedTools = unionStrings(base.RecommendedTools, p.RecommendedTools)
		p.Skills = unionStrings(base.Skills, p.Skills)
		p.Plugins = unionStrings(base.Plugins, p.Plugins)
	} else {
		p.Body = body
	}
	return &p, nil
}

// unionStrings returns a ∪ b preserving order (a first, then b's
// new entries), dropping duplicates. nil when both are empty.
func unionStrings(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, lst := range [][]string{a, b} {
		for _, v := range lst {
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// EffectiveTools is the persona's full tool-promotion set: union(Tools,
// RecommendedTools), deduped, Tools first. These names/globs are merged
// into the per-turn autoload surface when the persona is active (additive
// — they never hide a globally-available tool). recommended_tools, parsed
// but previously unused, is honored here as a second tools source.
func (p *Persona) EffectiveTools() []string {
	if p == nil {
		return nil
	}
	return unionStrings(p.Tools, p.RecommendedTools)
}

// maxPersonaBytes caps a persona markdown file (bundled ones average 6–10 KB).
const maxPersonaBytes int64 = 1 << 20

// readSource walks the resolution order and returns the raw file
// bytes plus the source path. Bundled lookups produce SourcePath="".
func (r Resolver) readSource(name string) ([]byte, string, error) {
	if !validName(name) {
		return nil, "", fmt.Errorf("persona %q: invalid name", name)
	}
	for _, dir := range r.dirs() {
		path := filepath.Join(dir, name+".md")
		// Reject symlinks (Lstat + IsRegular) and oversize files: project
		// .stado/personas/ is repo-controlled, and a bare ReadFile would
		// follow a symlinked default.md to an arbitrary local file and splice
		// it into the system prompt (exfil).
		info, lerr := os.Lstat(path)
		if lerr != nil || !info.Mode().IsRegular() || info.Size() > maxPersonaBytes {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			return data, path, nil
		}
	}
	// Bundled fallback.
	if data, err := library.ReadFile("library/" + name + ".md"); err == nil {
		return data, "", nil
	}
	return nil, "", fmt.Errorf("persona %q: %w", name, ErrNotFound)
}

func (r Resolver) dirs() []string {
	var out []string
	if r.CWD != "" {
		out = append(out, filepath.Join(r.CWD, ".stado", personasSubdir))
	}
	if r.ConfigDir != "" {
		out = append(out, filepath.Join(r.ConfigDir, personasSubdir))
	}
	return out
}

// List returns all personas visible to the resolver, deduped by name
// (project shadows user shadows bundled). Each entry's Body is fully
// assembled (inheritance resolved).
func (r Resolver) List() ([]Persona, error) {
	seen := map[string]bool{}
	var out []Persona

	visit := func(name string) {
		if seen[name] {
			return
		}
		p, err := r.Load(name)
		if err != nil {
			// Skip broken personas in the list view; Load surfaces the
			// error per name when a caller picks one.
			return
		}
		seen[name] = true
		out = append(out, *p)
	}

	for _, dir := range r.dirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if name == e.Name() {
				continue // not a .md file
			}
			if !validName(name) {
				continue
			}
			visit(name)
		}
	}
	// Bundled.
	bundled, err := fs.ReadDir(library, "library")
	if err == nil {
		for _, e := range bundled {
			name := strings.TrimSuffix(e.Name(), ".md")
			if name == e.Name() {
				continue
			}
			visit(name)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Names returns just the resolvable persona names, sorted.
func (r Resolver) Names() []string {
	personas, err := r.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(personas))
	for _, p := range personas {
		names = append(names, p.Name)
	}
	return names
}

// validName guards against directory traversal and weird filenames.
// Allows lowercase letters, digits, hyphens, underscores. Length 1..64.
func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// parsePersona splits frontmatter from the markdown body.
// Frontmatter is delimited by `---` lines; body is everything after.
// A file with no frontmatter is valid — empty Persona, full body.
func parsePersona(raw []byte) (Persona, string, error) {
	s := string(raw)
	// Strip optional leading whitespace before the opening `---`.
	trimmed := strings.TrimLeft(s, "\n\r ")
	if !strings.HasPrefix(trimmed, "---") {
		return Persona{}, s, nil
	}
	// Find the closing `---` on its own line.
	rest := trimmed[3:]
	rest = strings.TrimLeft(rest, "\r")
	if !strings.HasPrefix(rest, "\n") {
		return Persona{}, s, nil
	}
	rest = rest[1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Persona{}, s, fmt.Errorf("frontmatter: no closing ---")
	}
	yamlText := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, "\r\n")
	var p Persona
	if err := yaml.Unmarshal([]byte(yamlText), &p); err != nil {
		return Persona{}, s, fmt.Errorf("frontmatter: %w", err)
	}
	return p, body, nil
}

// AssembleSystem composes the final system prompt for a turn:
//
//	persona body
//	(blank line)
//	project AGENTS.md / CLAUDE.md  (when non-empty)
//	(blank line)
//	memory context             (when non-empty)
//	(blank line)
//	per-call extra              (when non-empty)
//
// Sections are separated by a blank line; missing sections are
// elided cleanly. Trailing whitespace trimmed.
func AssembleSystem(p *Persona, projectInstructions, memoryCtx, extra string) string {
	var parts []string
	if p != nil && strings.TrimSpace(p.Body) != "" {
		parts = append(parts, strings.TrimSpace(p.Body))
	}
	for _, s := range []string{projectInstructions, memoryCtx, extra} {
		if t := strings.TrimSpace(s); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}
