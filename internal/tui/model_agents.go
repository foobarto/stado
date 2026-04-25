package tui

import (
	"fmt"

	"github.com/foobarto/stado/internal/tui/agentpicker"
	"github.com/foobarto/stado/internal/tui/filepicker"
)

func (m inputMode) agentID() string {
	switch m {
	case modePlan:
		return "plan"
	case modeBTW:
		return "btw"
	default:
		return "do"
	}
}

func agentPickerItems(current inputMode) []agentpicker.Item {
	return []agentpicker.Item{
		{
			ID:      "do",
			Name:    "Do",
			Desc:    "all configured tools",
			Current: current == modeDo,
		},
		{
			ID:      "plan",
			Name:    "Plan",
			Desc:    "read-only planning tools",
			Current: current == modePlan,
		},
		{
			ID:      "btw",
			Name:    "BTW",
			Desc:    "off-band side question",
			Current: current == modeBTW,
		},
	}
}

func (m *Model) openAgentPicker() {
	m.agentPick.Open(agentPickerItems(m.mode), m.mode.agentID())
}

func (m *Model) filePickerAgentItems() []filepicker.Item {
	items := agentPickerItems(m.mode)
	out := make([]filepicker.Item, 0, len(items))
	for _, item := range items {
		out = append(out, filepicker.Item{
			Kind:    filepicker.KindAgent,
			ID:      item.ID,
			Display: item.Name,
			Meta:    item.Desc,
		})
	}
	return out
}

func (m *Model) filePickerContextItems() []filepicker.Item {
	out := m.filePickerAgentItems()
	out = append(out, m.filePickerSessionItems()...)
	out = append(out, m.filePickerSkillItems()...)
	out = append(out, m.filePickerDocItems()...)
	return out
}

func (m *Model) setAgentMode(id string) error {
	switch id {
	case "do":
		m.mode = modeDo
	case "plan":
		m.mode = modePlan
	case "btw":
		m.mode = modeBTW
	default:
		return fmt.Errorf("unknown agent: %s", id)
	}
	return nil
}
