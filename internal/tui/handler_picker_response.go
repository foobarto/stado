package tui

// Picker-active tea.KeyPressMsg dispatch. When any picker / palette overlay is
// visible, every keystroke goes here first. Each picker takes
// ownership of its keys: the textarea below stays untouched, and
// pickers that emit selections wire their results back into the
// model (model switch, persona apply, theme apply, session
// switch/fork/rename/delete, fleet view/cancel/remove, etc.).
//
// The handler returns a bool — true means a picker consumed (or
// swallowed) the keypress and the caller should short-circuit. False
// means no picker is open and the caller continues with the
// non-picker branches of tea.KeyPressMsg dispatch.

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/tui/fleetpicker"
	"github.com/foobarto/stado/internal/tui/keys"
)

func onPickerKey(m *Model, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.slash.Visible {
		// Palette owns all keypresses while visible — keystrokes feed
		// its internal Query (so characters don't leak into the main
		// textarea beneath the modal).
		cmd, handled := m.slash.Update(msg)
		if handled {
			if !m.slash.Visible {
				m.slashInline = false
			}
			return m, cmd, true
		}
		if m.keys.Matches(msg, keys.InputSubmit) {
			// The palette captures every keystroke into its Query (the main
			// textarea stays empty while it's visible), so on Enter we must
			// run what the user actually typed — not just the highlighted
			// suggestion's name. Previously this dispatched sel.Name, which
			// dropped every argument: "/tool fs.read" ran "/tool", and an
			// unmatched command did nothing. Decide what to run:
			//   - args typed (a space in the query) → the literal command
			//   - bare prefix with a highlighted suggestion → that suggestion
			//   - bare text with no match → run it (so unknown cmds error)
			// The query may already carry a leading "/" when the palette was
			// opened via Ctrl+P and the user typed "/tool" themselves (the
			// inline "/" trigger strips it; Ctrl+P doesn't) — trim it so we
			// don't dispatch "//tool".
			raw := strings.TrimSpace(m.slash.Query)
			q := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
			sel := m.slash.Selected()
			var cmd string
			switch {
			case strings.ContainsRune(q, ' '):
				cmd = "/" + q
			case sel != nil:
				cmd = sel.Name
			case q != "":
				cmd = "/" + q
			default:
				return m, nil, true // bare "/" + Enter: nothing to run
			}
			m.slash.Close()
			m.slashInline = false
			return m, m.handleSlash(cmd), true
		}
		// Any other keys swallowed so they don't reach the input.
		return m, nil, true
	}

	if m.agentPick.Visible {
		cmd, handled := m.agentPick.Update(msg)
		if handled {
			return m, cmd, true
		}
		if m.keys.Matches(msg, keys.InputSubmit) {
			if sel := m.agentPick.Selected(); sel != nil {
				m.agentPick.Close()
				if err := m.setAgentMode(sel.ID); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				m.layout()
				return m, nil, true
			}
		}
		return m, nil, true
	}

	// Fleet picker is modal — route keypresses and act on the Result
	// the picker emits. Esc / Ctrl+G fall through to the
	// SessionInterrupt handler below; Ctrl+C is handled at the top of
	// Update via closeAllModals.
	if m.fleetPicker != nil && m.fleetPicker.Visible {
		cmd, handled := m.fleetPicker.Update(msg)
		if !handled && msg.String() == "esc" {
			m.fleetPicker.Close()
			m.layout()
			return m, nil, true
		}
		if m.fleetPicker.Out.Action != fleetpicker.ActionNone {
			out := m.fleetPicker.Out
			m.fleetPicker.Out = fleetpicker.Result{}
			switch out.Action {
			case fleetpicker.ActionView:
				if e, ok := m.fleet.Get(out.FleetID); ok && e.SessionID != "" {
					m.fleetPicker.Close()
					m.appendBlock(block{kind: "system",
						body: "fleet: switch to session " + e.SessionID + " — use `/session " + e.SessionID + "`"})
					m.renderBlocks()
				} else {
					m.appendBlock(block{kind: "system",
						body: "fleet: child session id not yet known (still spawning)"})
					m.renderBlocks()
				}
			case fleetpicker.ActionCancel:
				_ = m.fleet.Cancel(out.FleetID)
				m.fleetPicker.Refresh(m.fleet.List())
				m.appendBlock(block{kind: "system",
					body: "fleet: cancelled background agent " + shortFleetID(out.FleetID)})
				m.renderBlocks()
			case fleetpicker.ActionRemove:
				m.fleet.Remove(out.FleetID)
				m.fleetPicker.Refresh(m.fleet.List())
			}
		}
		if handled {
			return m, cmd, true
		}
		return m, nil, true
	}

	// Model picker is modal too — same routing pattern as palette.
	if m.modelPicker.Visible {
		if msg.String() == "ctrl+a" {
			m.showSelectedProviderSetup()
			return m, nil, true
		}
		if msg.String() == "ctrl+f" {
			if sel := m.modelPicker.Selected(); sel != nil {
				favorite := m.toggleModelFavorite(*sel)
				m.modelPicker.SetFavorite(sel.ID, sel.ProviderName, favorite)
				m.layout()
			}
			return m, nil, true
		}
		cmd, handled := m.modelPicker.Update(msg)
		if handled {
			return m, cmd, true
		}
		if m.keys.Matches(msg, keys.InputSubmit) {
			if sel := m.modelPicker.Selected(); sel != nil {
				old := m.model
				oldProvider := m.providerName
				m.model = sel.ID

				// Provider switch: when the selected model came from a
				// different provider (typically a detected local
				// runner), the user almost certainly wants the backend
				// to switch too. Otherwise picking
				// "lmstudio · detected" still routes to anthropic on
				// the next prompt.
				providerSwitched := false
				if sel.ProviderName != "" && sel.ProviderName != oldProvider {
					m.providerName = sel.ProviderName
					m.provider = nil // force rebuild via buildProvider on next ensureProvider
					// Reset the token-counter probe so we re-check
					// against the new backend's capabilities.
					m.tokenCounterChecked = false
					providerSwitched = true
				}

				m.rememberModelSelection(*sel)
				m.modelPicker.Close()
				body := "model: " + old + " → " + m.model + "  (" + sel.Origin + ")"
				if providerSwitched {
					body += "\nprovider: " + oldProvider + " → " + m.providerName
				}
				if err := m.persistDefaultModel(m.providerName, m.model); err != nil {
					body += "\n" + err.Error()
				}
				m.appendBlock(block{kind: "system", body: body})
				m.layout()
				return m, nil, true
			}
		}
		return m, nil, true
	}

	if m.personaPicker.Visible {
		cmd, handled := m.personaPicker.Update(msg)
		if handled {
			return m, cmd, true
		}
		if m.keys.Matches(msg, keys.InputSubmit) {
			if sel := m.personaPicker.Selected(); sel != nil {
				m.personaPicker.Close()
				m.applyPersonaSelection(sel.ID)
				m.layout()
			}
			return m, nil, true
		}
		return m, nil, true
	}

	if m.sessionPick.Visible {
		if m.sessionPick.Renaming() {
			if m.keys.Matches(msg, keys.InputSubmit) {
				target := m.sessionPick.Target()
				if err := m.renameSession(target.ID, m.sessionPick.RenameValue()); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				m.sessionPick.CancelAction()
				if err := m.openSessionPicker(); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				return m, nil, true
			}
			cmd, _ := m.sessionPick.Update(msg)
			return m, cmd, true
		}
		if m.sessionPick.Deleting() {
			if m.keys.Matches(msg, keys.InputSubmit) || yesKey(msg) {
				target := m.sessionPick.Target()
				if target.Current {
					return m, nil, true
				}
				if err := m.deleteSession(target.ID); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				m.sessionPick.CancelAction()
				if err := m.openSessionPicker(); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				return m, nil, true
			}
			if noKey(msg) {
				m.sessionPick.CancelAction()
				return m, nil, true
			}
			cmd, _ := m.sessionPick.Update(msg)
			return m, cmd, true
		}
		switch msg.String() {
		case "ctrl+n":
			m.sessionPick.Close()
			if err := m.createAndSwitchSession(); err != nil {
				m.appendBlock(block{kind: "system", body: err.Error()})
				m.renderBlocks()
			}
			return m, nil, true
		case "ctrl+r":
			m.sessionPick.BeginRename()
			m.layout()
			return m, nil, true
		case "ctrl+d":
			m.sessionPick.BeginDelete()
			m.layout()
			return m, nil, true
		case "ctrl+f":
			if sel := m.sessionPick.Selected(); sel != nil {
				m.sessionPick.Close()
				if err := m.forkAndSwitchSession(sel.ID); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
			}
			return m, nil, true
		}
		cmd, handled := m.sessionPick.Update(msg)
		if handled {
			return m, cmd, true
		}
		if m.keys.Matches(msg, keys.InputSubmit) {
			if sel := m.sessionPick.Selected(); sel != nil {
				m.sessionPick.Close()
				if err := m.switchToSession(sel.ID); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				return m, nil, true
			}
		}
		return m, nil, true
	}

	if m.taskPick.Visible {
		cmd, handled := m.taskPick.Update(msg)
		if handled {
			if err := m.applyTaskCommand(cmd); err != nil {
				m.taskPick.SetNotice(err.Error())
			}
			m.layout()
			return m, nil, true
		}
		return m, nil, true
	}

	if m.providerPick != nil && m.providerPick.Visible {
		cmd, handled := m.providerPick.Update(msg)
		if handled {
			if err := m.applyProviderCommand(cmd); err != nil {
				m.providerPick.SetNotice(err.Error())
			}
			m.layout()
			return m, nil, true
		}
		return m, nil, true
	}

	if m.themePick.Visible {
		cmd, handled := m.themePick.Update(msg)
		if handled {
			return m, cmd, true
		}
		if m.keys.Matches(msg, keys.InputSubmit) {
			if sel := m.themePick.Selected(); sel != nil {
				m.themePick.Close()
				if sel.Current && sel.Mode == "custom" {
					return m, nil, true
				}
				if err := m.applyNamedTheme(sel.ID); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				return m, nil, true
			}
		}
		return m, nil, true
	}

	// Filepicker popover owns navigation keys while visible so Up/Down
	// don't scroll the textarea and Tab/Enter accept the highlighted
	// path instead of inserting literal whitespace or submitting a
	// half-written prompt. Esc closes without inserting. Anything else
	// falls through so typing refines the query naturally.
	//
	// Note: filePicker is unique in that *not* every key it sees is
	// consumed — typing characters falls through to the editor
	// (refining the query). The caller checks `handled=false` here and
	// continues to the textarea path.
	if m.filePicker.Visible {
		if cmd, handled := m.filePicker.Update(msg); handled {
			return m, cmd, true
		}
		switch msg.String() {
		case "esc":
			m.filePicker.Close()
			return m, nil, true
		case "tab":
			m.acceptFilePickerSelection()
			return m, nil, true
		case "enter":
			if m.filePicker.Selected() != "" {
				m.acceptFilePickerSelection()
				return m, nil, true
			}
		}
		// Other keys flow through to refine the query in the editor.
	}

	return m, nil, false
}
