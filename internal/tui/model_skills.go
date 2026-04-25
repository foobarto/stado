package tui

import (
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tui/filepicker"
	"github.com/foobarto/stado/pkg/agent"
)

func (m *Model) filePickerSkillItems() []filepicker.Item {
	out := make([]filepicker.Item, 0, len(m.skills))
	for _, sk := range m.skills {
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
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, chosen.Body))
	m.appendBlock(block{kind: "user", body: chosen.Body})
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
