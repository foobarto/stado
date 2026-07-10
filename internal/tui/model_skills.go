package tui

import (
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tui/filepicker"
	"github.com/foobarto/stado/pkg/agent"
)

func (m *Model) filePickerSkillItems() []filepicker.Item {
	out := make([]filepicker.Item, 0, len(m.skills))
	for _, sk := range skills.UserVisible(m.skills) {
		meta := sk.Description
		if meta == "" {
			meta = "skill prompt"
		}
		out = append(out, filepicker.Item{
			Kind:    filepicker.KindSkill,
			ID:      sk.Name,
			Display: sk.Name,
			Meta:    meta,
			Insert:  "/skill:" + sk.Name,
		})
	}
	return out
}

func (m *Model) findSkill(name string) *skills.Skill {
	for i := range m.skills {
		if m.skills[i].Name == name {
			return &m.skills[i]
		}
	}
	return nil
}

func (m *Model) injectSkill(name string) error {
	chosen := m.findSkill(name)
	if chosen == nil {
		return fmt.Errorf("skill %q not found - try /skill for the list", name)
	}
	if !chosen.UserInvocable {
		return fmt.Errorf("skill %q is model-only and cannot be invoked with /skill", name)
	}
	body := chosen.RenderedBody()
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, body))
	m.appendBlock(block{kind: "user", body: body})
	m.renderBlocks()
	return nil
}

func consumeMentionDraft(val string, anchor, cursor int) string {
	before := val[:anchor]
	after := strings.TrimLeft(val[cursor:], " \t")
	if strings.TrimSpace(before) == "" {
		before = ""
	}
	if before != "" && after != "" && !strings.HasSuffix(before, " ") && !strings.HasSuffix(before, "\n") {
		before += " "
	}
	return before + after
}

// skillModelInvocationEnabled reports whether the model-facing skill surface
// (system-prompt listing + skills__load autoload) should appear this turn.
// Mirrors runtime.SkillModelInvocationEnabled but also respects in-session
// /tool disable overrides — otherwise the listing advertises skills__load
// while the per-turn slate hides it.
func (m *Model) skillModelInvocationEnabled() bool {
	if m.executor == nil {
		return false
	}
	if !runtime.SkillModelInvocationEnabled(m.executor.Registry, m.skills) {
		return false
	}
	return !m.sessionToolOverrideHidesTool("skills__load")
}

// toolNameForID maps a tool-call block id back to the tool name, or "" if no
// tool block matches. Same lookup onToolResult uses to special-case results.
func (m *Model) toolNameForID(id string) string {
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == id {
			return m.blocks[i].toolName
		}
	}
	return ""
}

// absorbSkillLoads trims skills__load tool results and returns skill bodies to
// inject as user messages (EP-0045). Gated on the originating tool name so a
// non-skills__load tool/plugin that happens to return JSON shaped like
// SkillLoadResponse ({"loaded":true,"body":"..."}) is never mis-absorbed and
// its output injected as a user message — mirrors the runtime loop's
// `c.Name == "skills__load"` check.
func (m *Model) absorbSkillLoads(results []agent.ToolResultBlock) []string {
	var injections []string
	for i := range results {
		if results[i].IsError {
			continue
		}
		if m.toolNameForID(results[i].ToolUseID) != "skills__load" {
			continue
		}
		if body, trimmed := runtime.AbsorbSkillLoad(results[i].Content); body != "" {
			results[i].Content = trimmed
			injections = append(injections, body)
		}
	}
	return injections
}
