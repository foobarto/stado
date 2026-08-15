package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// writeRawSkill drops a `.stado/skills/<file>.md` under root with verbatim
// content — distinct from the existing writeSkill helper, which assembles
// only name/description frontmatter and can't carry a `slash:` field.
func writeRawSkill(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSkillSlashModel(t *testing.T, cwd string) *Model {
	t.Helper()
	// Each test owns the package-global dynamic palette layer; clear it
	// on cleanup so cross-test ordering doesn't leak shortcuts.
	t.Cleanup(func() { palette.RegisterDynamicCommands(nil) })
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	prov := &captureReqProvider{done: make(chan struct{})}
	return NewModel(cwd, "m", "p", func() (agent.Provider, error) { return prov, nil }, rnd, keys.NewRegistry())
}

func dynamicHasCommand(name string) bool {
	for _, c := range palette.DynamicCommands() {
		if c.Name == name {
			return true
		}
	}
	return false
}

// TestSkillSlash_RegistersAndAppears: a skill with `slash:` shows up in
// the palette's dynamic layer (hence in both the "/" popup and Ctrl+P),
// and the /<name> → skill-name dispatch map is populated.
func TestSkillSlash_RegistersAndAppears(t *testing.T) {
	root := t.TempDir()
	writeRawSkill(t, root, "refactor.md",
		"---\nname: refactor\ndescription: Extract a helper\nslash: rf\n---\nFactor out the duplication.")

	m := newSkillSlashModel(t, root)

	if got := m.skillSlash["rf"]; got != "refactor" {
		t.Fatalf("skillSlash[rf] = %q, want refactor", got)
	}
	if !dynamicHasCommand("/rf") {
		t.Fatalf("/rf not registered in the palette dynamic layer: %+v", palette.DynamicCommands())
	}

	// The palette Model surfaces it in its match list.
	m.slash.Open()
	found := false
	for _, c := range m.slash.Matches {
		if c.Name == "/rf" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/rf not in palette matches — dynamic command not reachable in the popup/panel")
	}
}

// TestSkillSlash_DispatchInvokesSkill: typing /rf runs the skill via the
// existing skill-invocation path (injects the body as a user message).
func TestSkillSlash_DispatchInvokesSkill(t *testing.T) {
	root := t.TempDir()
	body := "Factor out the duplication near the cursor."
	writeRawSkill(t, root, "refactor.md",
		"---\nname: refactor\nslash: rf\n---\n"+body)

	m := newSkillSlashModel(t, root)

	before := len(m.msgs)
	m.handleSlash("/rf")
	if len(m.msgs) != before+1 {
		t.Fatalf("dispatch did not inject a message: msgs %d → %d", before, len(m.msgs))
	}
	last := m.msgs[len(m.msgs)-1]
	if len(last.Content) == 0 || last.Content[0].Text == nil || last.Content[0].Text.Text != body {
		t.Fatalf("injected message = %+v, want skill body %q", last, body)
	}
}

func TestSkillSlash_OwnerPrecedesStaleAlias(t *testing.T) {
	root := t.TempDir()
	body := "Use the skill owner, not the stale alias."
	writeRawSkill(t, root, "owner.md", "---\nname: owner\nslash: owned\n---\n"+body)
	m := newSkillSlashModel(t, root)
	m.cfg = &config.Config{Aliases: config.Aliases{"owned": "/help"}}

	before := len(m.msgs)
	m.handleSlash("/owned")
	if len(m.msgs) != before+1 {
		t.Fatalf("stale alias shadowed skill owner: msgs %d → %d", before, len(m.msgs))
	}
	last := m.msgs[len(m.msgs)-1]
	if len(last.Content) == 0 || last.Content[0].Text == nil || last.Content[0].Text.Text != body {
		t.Fatalf("skill owner injected %+v, want %q", last, body)
	}
}

// TestSkillSlash_UnknownStillErrors: a /<name> that isn't a registered
// skill shortcut still falls through to the unknown-command error.
func TestSkillSlash_UnknownStillErrors(t *testing.T) {
	root := t.TempDir()
	m := newSkillSlashModel(t, root)
	before := len(m.blocks)
	m.handleSlash("/definitelynotacommand")
	if len(m.blocks) != before+1 {
		t.Fatalf("expected one system block for unknown command")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "unknown command") {
		t.Fatalf("expected unknown-command system block, got %+v", last)
	}
}

// TestSkillSlash_BuiltinCollisionRejected: a skill whose `slash:` shadows
// a built-in is rejected — no dynamic command, no dispatch entry. The
// rejection is surfaced (here we assert it never registered; the warning
// goes to stderr at build-time).
func TestSkillSlash_BuiltinCollisionRejected(t *testing.T) {
	root := t.TempDir()
	// /help is a built-in palette row; /clear too. /quit is reserved but
	// has no palette row — both must be rejected.
	writeRawSkill(t, root, "shadow_help.md", "---\nname: shadowhelp\nslash: help\n---\nx")
	writeRawSkill(t, root, "shadow_quit.md", "---\nname: shadowquit\nslash: quit\n---\nx")
	writeRawSkill(t, root, "ok.md", "---\nname: okskill\nslash: okcmd\n---\ny")

	m := newSkillSlashModel(t, root)

	if _, ok := m.skillSlash["help"]; ok {
		t.Errorf("slash: help collided with a built-in but was registered")
	}
	if _, ok := m.skillSlash["quit"]; ok {
		t.Errorf("slash: quit collided with a reserved command but was registered")
	}
	if dynamicHasCommand("/help") {
		// /help would already be a built-in; the dynamic layer must not
		// also carry a /help row.
		t.Errorf("dynamic layer shadows built-in /help")
	}
	// The non-colliding skill still registers.
	if m.skillSlash["okcmd"] != "okskill" {
		t.Errorf("non-colliding slash: okcmd not registered: %+v", m.skillSlash)
	}
}

func TestSuperviseCommandIsDynamicallyOwned(t *testing.T) {
	if IsReservedSlashName("/supervise") {
		t.Fatal("/supervise remains reserved after lifecycle-application cutover")
	}
	if palette.CheckSlashCollision("/supervise") {
		t.Fatal("/supervise remains in the static palette after lifecycle-application cutover")
	}
	loaded := &stadoruntime.LoadedLifecycleApplication{
		Identity: plugins.RuntimeIdentity{Canonical: "github.com/foobarto/stado-plugins/supervise@v0.1.0"},
		Manifest: plugins.Manifest{Commands: []plugins.CommandDef{{Name: "supervise", Description: "quality gate"}}},
	}
	m := newSkillSlashModel(t, t.TempDir())
	m.applicationCommands = map[string]*stadoruntime.LoadedLifecycleApplication{"supervise": loaded}
	m.registerSkillSlashCommands(func(string) {})
	if !dynamicHasCommand("/supervise") {
		t.Fatalf("admitted application command is not dynamically discoverable: %+v", palette.DynamicCommands())
	}
	m.applicationCommands = nil
	m.registerSkillSlashCommands(func(string) {})
	if dynamicHasCommand("/supervise") {
		t.Fatalf("removed application left stale dynamic command: %+v", palette.DynamicCommands())
	}
}

// TestSkillSlash_ReloadRefreshes: adding a skill on disk and re-deriving
// (the /reload path) picks up the new shortcut; removing one drops it.
func TestSkillSlash_ReloadRefreshes(t *testing.T) {
	root := t.TempDir()
	writeRawSkill(t, root, "a.md", "---\nname: aa\nslash: aa\n---\nbody a")

	m := newSkillSlashModel(t, root)
	if !dynamicHasCommand("/aa") {
		t.Fatalf("/aa missing after initial load")
	}

	// Add a second skill, then re-derive (simulating /reload's skill
	// re-read + re-register; we call the same helpers directly to avoid
	// a full config.Load roundtrip).
	writeRawSkill(t, root, "b.md", "---\nname: bb\nslash: bb\n---\nbody b")
	if sks, err := skills.Load(m.cwd); err == nil {
		m.skills = sks
	} else {
		t.Fatal(err)
	}
	m.registerSkillSlashCommands(func(string) {})

	if !dynamicHasCommand("/aa") || !dynamicHasCommand("/bb") {
		t.Fatalf("reload did not pick up both shortcuts: %+v", palette.DynamicCommands())
	}
	if m.skillSlash["bb"] != "bb" {
		t.Fatalf("dispatch map missing /bb after reload")
	}

	// Remove the first skill and re-derive — its shortcut should drop.
	if err := os.Remove(filepath.Join(root, ".stado", "skills", "a.md")); err != nil {
		t.Fatal(err)
	}
	if sks, err := skills.Load(m.cwd); err == nil {
		m.skills = sks
	} else {
		t.Fatal(err)
	}
	m.registerSkillSlashCommands(func(string) {})

	if dynamicHasCommand("/aa") {
		t.Fatalf("reload did not drop the removed /aa shortcut")
	}
	if _, ok := m.skillSlash["aa"]; ok {
		t.Fatalf("dispatch map still has /aa after removal")
	}
	if !dynamicHasCommand("/bb") {
		t.Fatalf("reload dropped the still-present /bb shortcut")
	}
}
