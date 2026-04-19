package keys

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/key"
)

type Registry struct {
	mu       sync.RWMutex
	bindings map[Action][]key.Binding
}

func NewRegistry() *Registry {
	r := &Registry{
		bindings: make(map[Action][]key.Binding),
	}
	r.loadDefaults()
	return r
}

func (r *Registry) loadDefaults() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for action, keysStr := range Defaults {
		desc := ActionDescriptions[action]
		if desc == "" {
			desc = string(action)
		}
		r.bindings[action] = Parse(keysStr, desc)
	}
}

func (r *Registry) Get(action Action) []key.Binding {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bindings[action]
}

func (r *Registry) Matches(msg tea.KeyMsg, action Action) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bindings, ok := r.bindings[action]
	if !ok {
		return false
	}

	for _, b := range bindings {
		if key.Matches(msg, b) {
			return true
		}
	}
	return false
}

// ActionsByGroup returns bindings grouped for the help overlay
func (r *Registry) ActionsByGroup() map[string][]Action {
	return map[string][]Action{
		"App": {
			AppExit, TipsToggle, CommandList,
		},
		"Session": {
			SessionInterrupt, Approve, Deny, EditSummary,
		},
		"Input Navigation": {
			InputMoveLeft, InputMoveRight, InputWordBackward, InputWordForward,
			InputLineHome, InputLineEnd,
		},
		"Input Editing": {
			InputClear, InputSubmit, InputNewline, InputBackspace, InputDelete,
			InputDeleteToLineEnd, InputDeleteToLineStart, InputDeleteWordBackward, InputDeleteWordForward,
		},
		"History": {
			HistoryPrevious, HistoryNext,
		},
		"Messages View": {
			MessagesPageUp, MessagesPageDown, MessagesHalfPageUp, MessagesHalfPageDown,
			MessagesFirst, MessagesLast,
		},
	}
}
