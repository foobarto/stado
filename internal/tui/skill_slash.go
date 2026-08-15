package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tui/palette"
)

// skillSlashGroup is the palette group header skill-declared shortcuts
// render under, keeping them visually distinct from the built-in
// Quick/Session/View groups.
const skillSlashGroup = "Skills"
const applicationSlashGroup = "Applications"

// skillSlashCommands derives the dynamic palette commands from a set of
// loaded skills. Only skills with a non-empty `slash:` frontmatter field
// contribute a shortcut. A command is rejected (with a warning appended
// to warnings) when its name:
//   - collides with a built-in slash command (palette built-in OR a
//     reserved handleSlash branch with no palette row), or
//   - duplicates another skill's already-accepted shortcut (first wins,
//     matching the skills loader's nearest-wins name policy).
//
// The returned commands map 1:1 onto m.skillSlash so dispatch can resolve
// `/<name>` back to the owning skill name. warnings is nil when every
// `slash:` registered cleanly.
func skillSlashCommands(sks []skills.Skill) (cmds []palette.Command, byCommand map[string]string, warnings []string) {
	byCommand = make(map[string]string)
	for _, sk := range sks {
		if !sk.UserInvocable {
			continue
		}
		name := strings.TrimSpace(sk.Slash)
		if name == "" {
			continue
		}
		// `slash:` is a single command token: no spaces (no args), no
		// leading slash (we add it), letters/digits/_/- only — same
		// shape /alias enforces. Reject anything malformed loudly so a
		// typo doesn't silently vanish.
		if err := validateSkillSlashName(name); err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"skill %q (%s): slash: %q rejected — %v", sk.Name, sk.Path, name, err))
			continue
		}
		full := "/" + name
		// Collision against built-ins: palette rows AND the broader
		// reserved set (handleSlash branches without a palette entry,
		// e.g. /quit, /cancel). Reuse the /alias collision prior art.
		if palette.CheckSlashCollision(full) || IsReservedSlashName(full) {
			warnings = append(warnings, fmt.Sprintf(
				"skill %q (%s): slash: /%s shadows a built-in command and was rejected — rename the slash field",
				sk.Name, sk.Path, name))
			continue
		}
		if owner, dup := byCommand[name]; dup {
			warnings = append(warnings, fmt.Sprintf(
				"skill %q (%s): slash: /%s already claimed by skill %q — kept the first",
				sk.Name, sk.Path, name, owner))
			continue
		}
		byCommand[name] = sk.Name
		desc := sk.Description
		if desc == "" {
			desc = "Run the " + sk.Name + " skill"
		}
		cmds = append(cmds, palette.Command{
			Name:  full,
			Desc:  desc,
			Group: skillSlashGroup,
		})
	}
	// Stable order so the palette doesn't reshuffle between reloads.
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	return cmds, byCommand, warnings
}

// validateSkillSlashName enforces the same shape as /alias names: a
// single bare token of letters/digits/_/-, no spaces, non-empty. Kept
// local (rather than reusing config.ValidateAliasName) so the skills
// surface doesn't take a config dependency just for a regex.
func validateSkillSlashName(name string) error {
	if name == "" {
		return fmt.Errorf("empty")
	}
	if strings.ContainsAny(name, " \t") {
		return fmt.Errorf("must be a single token with no arguments")
	}
	for _, r := range name {
		if r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') {
			continue
		}
		return fmt.Errorf("only letters, digits, _ and - are allowed")
	}
	return nil
}

// registerSkillSlashCommands recomputes the skill-backed dynamic palette
// layer from m.skills, replaces the palette's dynamic commands, and
// refreshes m.skillSlash (the /<name> → skill-name dispatch map). It is
// called once at NewModel build-time and again on /reload so the shortcut
// list tracks the skills currently on disk.
//
// Warnings are surfaced via emit: at build-time emit writes to stderr
// (like instructions.Load / skills.Load); on /reload the caller passes a
// closure that appends a system block instead.
func (m *Model) registerSkillSlashCommands(emit func(string)) {
	cmds, byCommand, warnings := skillSlashCommands(m.skills)
	m.skillSlash = byCommand
	var applicationCommands []palette.Command
	for name, application := range m.applicationCommands {
		if application == nil {
			continue
		}
		description := "Run the " + application.Identity.Canonical + " application command"
		for _, command := range application.Manifest.Commands {
			if command.Name == name && strings.TrimSpace(command.Description) != "" {
				description = command.Description
				break
			}
		}
		applicationCommands = append(applicationCommands, palette.Command{
			Name: "/" + name, Desc: description, Group: applicationSlashGroup,
		})
	}
	sort.Slice(applicationCommands, func(i, j int) bool { return applicationCommands[i].Name < applicationCommands[j].Name })
	cmds = append(cmds, applicationCommands...)
	palette.RegisterDynamicCommands(cmds)
	for _, w := range warnings {
		emit(w)
	}
}

// stderrSkillSlashWarn is the build-time warning sink for skill-slash
// registration — matches the instructions/skills loader's stderr policy
// so a bad `slash:` field doesn't refuse to boot the TUI.
func stderrSkillSlashWarn(msg string) {
	fmt.Fprintf(os.Stderr, "stado: %s\n", msg)
}
