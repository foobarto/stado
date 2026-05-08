package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
)

// reservedSlashNames is the set of built-in TUI slash command
// names. Aliases that shadow any of these are rejected at create
// time so an operator can't footgun themselves into a session
// where `/help` does something unexpected. List is the union of
// every `case "/<name>":` branch in handleSlash + the prefix-style
// branches (/plugin, /skill, /tool, /t).
//
// New built-ins must be added here AND to the slash palette;
// otherwise an alias could shadow a brand-new built-in for
// existing users (their alias file pre-dates the new built-in).
// F-alias.
var reservedSlashNames = map[string]bool{
	"/adopt":     true,
	"/agents":    true,
	"/alias":     true,
	"/approvals": true,
	"/btw":       true,
	"/budget":    true,
	"/cancel":    true,
	"/clear":     true,
	"/compact":   true,
	"/config":    true,
	"/context":   true,
	"/debug":     true,
	"/describe":  true,
	"/exit":      true,
	"/fleet":     true,
	"/force":     true,
	"/help":      true,
	"/kill":      true,
	"/loop":      true,
	"/memory":    true,
	"/model":     true,
	"/monitor":   true,
	"/new":       true,
	"/persona":   true,
	"/plugin":    true,
	"/provider":  true,
	"/providers": true,
	"/ps":        true,
	"/queue-now": true,
	"/quit":      true,
	"/retry":     true,
	"/sandbox":   true,
	"/session":   true,
	"/sessions":  true,
	"/sidebar":   true,
	"/skill":     true,
	"/spawn":     true,
	"/split":     true,
	"/stats":     true,
	"/status":    true,
	"/stop":      true,
	"/subagents": true,
	"/supervisor": true,
	"/switch":    true,
	"/t":         true, // /tool alias
	"/task":      true,
	"/tasks":     true,
	"/theme":     true,
	"/thinking":  true,
	"/todo":      true,
	"/tool":      true,
	"/tools":     true,
	"/top":       true,
}

// IsReservedSlashName reports whether the given slash name (with
// leading "/") is a built-in. Used by /alias create to reject
// shadow attempts. F-alias.
func IsReservedSlashName(name string) bool {
	return reservedSlashNames[name]
}

// handleAliasSlash routes /alias create|list|rm subcommands. All
// changes go to the user-level config (~/.config/stado/config.toml)
// per operator's design choice — aliases are global, not project-
// scoped. F-alias.
func (m *Model) handleAliasSlash(parts []string) tea.Cmd {
	verb := ""
	if len(parts) >= 2 {
		verb = parts[1]
	}
	switch verb {
	case "", "list", "ls":
		m.appendBlock(block{kind: "system", body: renderAliasList(m.cfg)})
		return nil
	case "create", "add", "set":
		return m.handleAliasCreate(parts[2:])
	case "rm", "remove", "delete", "del":
		return m.handleAliasRemove(parts[2:])
	default:
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf(
				"/alias %s: unknown verb. Try: list, create, rm.\n\n"+
					"Examples:\n"+
					"  /alias create read /tool fs.read {\"path\":\"{1}\"}\n"+
					"  /alias list\n"+
					"  /alias rm read\n\n"+
					"Aliases are global (~/.config/stado/config.toml). Names that "+
					"shadow built-in slash commands are rejected. Positional args "+
					"use {1}, {2}, … in the expansion.",
				verb),
		})
		return nil
	}
}

// handleAliasCreate wires `/alias create <name> <expansion>`. Name
// validation enforces shape (`[a-zA-Z0-9_-]+`); expansion must
// start with `/`. Collision check rejects shadowing built-ins. The
// alias is persisted to the user config — current Model state isn't
// re-loaded; the next config.Load() picks it up. F-alias.
func (m *Model) handleAliasCreate(args []string) tea.Cmd {
	if len(args) < 2 {
		m.appendBlock(block{
			kind: "system",
			body: "usage: /alias create <name> <expansion>\n\n" +
				"  <name>      letters/digits/_/- only; written without leading /\n" +
				"  <expansion> full slash command, e.g. \"/tool fs.read {\\\"path\\\":\\\"{1}\\\"}\"\n\n" +
				"Positional args: {1}, {2}, … are substituted from the alias's call site.",
		})
		return nil
	}
	name := args[0]
	expansion := strings.Join(args[1:], " ")

	if err := config.ValidateAliasName(name); err != nil {
		m.appendBlock(block{kind: "system", body: "/alias create: " + err.Error()})
		return nil
	}
	if err := config.ValidateAliasExpansion(expansion); err != nil {
		m.appendBlock(block{kind: "system", body: "/alias create: " + err.Error()})
		return nil
	}

	// Collision: built-in slash command would be shadowed.
	if IsReservedSlashName("/" + name) {
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf(
				"/alias create: %q shadows a built-in slash command and was rejected. Pick a different name. (Run /help for the built-in list.)",
				name),
		})
		return nil
	}

	path := config.DefaultConfigPath()
	if err := config.WriteAliasAdd(path, name, expansion); err != nil {
		m.appendBlock(block{kind: "system", body: "/alias create: " + err.Error()})
		return nil
	}
	// Refresh the in-memory config so the new alias resolves on the
	// next slash without a session restart.
	if cfg, err := config.Load(); err == nil {
		m.cfg = cfg
	}
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("/alias create: /%s → %q (written to %s)", name, expansion, path),
	})
	return nil
}

// handleAliasRemove wires `/alias rm <name>`. Idempotent — removing
// a non-existent alias is not an error so scripted cleanup works.
// F-alias.
func (m *Model) handleAliasRemove(args []string) tea.Cmd {
	if len(args) < 1 {
		m.appendBlock(block{kind: "system", body: "usage: /alias rm <name>"})
		return nil
	}
	name := args[0]
	if err := config.ValidateAliasName(name); err != nil {
		m.appendBlock(block{kind: "system", body: "/alias rm: " + err.Error()})
		return nil
	}
	path := config.DefaultConfigPath()
	if err := config.WriteAliasRemove(path, name); err != nil {
		m.appendBlock(block{kind: "system", body: "/alias rm: " + err.Error()})
		return nil
	}
	if cfg, err := config.Load(); err == nil {
		m.cfg = cfg
	}
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("/alias rm: removed /%s (or no-op if absent) from %s", name, path),
	})
	return nil
}

// positionalRefRE matches `{N}` where N is one or more digits.
// Used by substituteAliasPositionals to expand operator-supplied
// positional args into the alias's expansion template. F-alias.
var positionalRefRE = regexp.MustCompile(`\{(\d+)\}`)

// substituteAliasPositionals replaces {1}, {2}, … in template with
// args[N-1]. Returns an error when a {N} reference exceeds the
// supplied arg count so an operator typing `/read` (no positional)
// against an alias `/tool fs.read {"path":"{1}"}` gets a clear
// error rather than literal `{1}` reaching the dispatcher. F-alias.
func substituteAliasPositionals(template string, args []string) (string, error) {
	var firstErr error
	out := positionalRefRE.ReplaceAllStringFunc(template, func(match string) string {
		// match is `{N}`; trim the braces and parse.
		n, err := strconv.Atoi(match[1 : len(match)-1])
		if err != nil || n < 1 {
			if firstErr == nil {
				firstErr = fmt.Errorf("invalid positional reference %s in alias expansion", match)
			}
			return match
		}
		if n > len(args) {
			if firstErr == nil {
				firstErr = fmt.Errorf("alias references %s but only %d positional arg(s) supplied", match, len(args))
			}
			return match
		}
		return args[n-1]
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// tryExpandAlias inspects the first token of parts. If the token
// (without leading "/") matches an alias in cfg AND that name does
// not collide with a built-in slash command, returns the expanded
// command line with positional substitution applied.
//
// The defensive built-in shadow check exists because /alias create
// already rejects collisions; this guards against an operator who
// hand-edited config.toml to plant a shadow.
//
// Returns (expanded, ok, err):
//   - ok=false when no alias matched or input was empty;
//   - ok=true, err=nil when the alias expanded cleanly;
//   - ok=true, err!=nil when a {N} positional couldn't be filled.
//
// Caller uses the err to surface a precise message and skip
// downstream dispatch. F-alias.
func tryExpandAlias(parts []string, cfg *config.Config) (string, bool, error) {
	if len(parts) == 0 || cfg == nil || len(cfg.Aliases) == 0 {
		return "", false, nil
	}
	first := parts[0]
	if !strings.HasPrefix(first, "/") {
		return "", false, nil
	}
	if IsReservedSlashName(first) {
		return "", false, nil
	}
	name := strings.TrimPrefix(first, "/")
	expansion, ok := cfg.Aliases[name]
	if !ok {
		return "", false, nil
	}
	expanded, err := substituteAliasPositionals(expansion, parts[1:])
	if err != nil {
		return "", true, err
	}
	return expanded, true, nil
}

// renderAliasList formats the cfg.Aliases map as a stable tabular
// listing for the system-block view. F-alias.
func renderAliasList(cfg *config.Config) string {
	if cfg == nil || len(cfg.Aliases) == 0 {
		return "No aliases defined. Run `/alias create <name> <expansion>` to add one."
	}
	names := make([]string, 0, len(cfg.Aliases))
	for k := range cfg.Aliases {
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("Aliases:")
	for _, n := range names {
		sb.WriteString(fmt.Sprintf("\n  /%s\t→ %s", n, cfg.Aliases[n]))
	}
	sb.WriteString("\n\nRun `/alias rm <name>` to remove. Names are written without the leading /.")
	return sb.String()
}
