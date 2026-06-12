package keys

import (
	"sort"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type Registry struct {
	mu            sync.Mutex
	bindings      map[Action][]key.Binding
	prefixes      map[Action][]PrefixBinding
	prefixState   []string
	prefixMatches map[Action]bool
}

func NewRegistry() *Registry {
	r := &Registry{
		bindings: make(map[Action][]key.Binding),
		prefixes: make(map[Action][]PrefixBinding),
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
		r.prefixes[action] = ParsePrefix(keysStr, desc)
	}
}

func (r *Registry) Get(action Action) []key.Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bindings[action]
}

func (r *Registry) Prefixes(action Action) []PrefixBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prefixes[action]
}

// HelpKeys returns the user-facing key strings for an action.
// Prefix bindings are shown as their full chord sequence (for
// example "ctrl+x ctrl+b"), while the synthetic first-chord
// bindings used for bubbletea internals are hidden.
func (r *Registry) HelpKeys(action Action) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	bindings := r.bindings[action]
	prefixes := r.prefixes[action]

	prefixFirst := make(map[string]bool, len(prefixes))
	for _, pb := range prefixes {
		if len(pb.Chords) > 0 {
			prefixFirst[pb.Chords[0]] = true
		}
	}

	seen := make(map[string]bool, len(bindings)+len(prefixes))
	var out []string
	for _, kb := range bindings {
		key := kb.Help().Key
		if prefixFirst[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	for _, pb := range prefixes {
		if pb.HelpKey == "" || seen[pb.HelpKey] {
			continue
		}
		seen[pb.HelpKey] = true
		out = append(out, pb.HelpKey)
	}
	return out
}

// Matches checks whether msg matches any flat binding for action.
// Prefix sequences are NOT handled here; use IsPrefixChord / TryPrefix
// for chord dispatch.
func (r *Registry) Matches(msg tea.KeyPressMsg, action Action) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

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

// IsPrefixChord returns true if msg matches the FIRST chord of any
// registered prefix binding.  Called at the top of key dispatch to
// decide whether to consume a keystroke as a prefix primer.
func (r *Registry) IsPrefixChord(msg tea.KeyPressMsg) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, pbs := range r.prefixes {
		for _, pb := range pbs {
			if len(pb.Chords) > 0 && chordMatches(msg, pb.Chords[0]) {
				return true
			}
		}
	}
	return false
}

// TryPrefix consumes one keystroke as part of a prefix sequence.
//   - If not currently in a prefix:  checks whether msg starts any
//     prefix chord.  Returns ("", true) if it does (state saved).
//     Returns ("", false) if no prefix matches.
//   - If already in a prefix:  appends the chord, then either
//     returns the completed action + true, returns ("", true) if
//     the sequence is still partial, or resets state + returns
//     ("", false) if nothing matches.
func (r *Registry) TryPrefix(msg tea.KeyPressMsg) (Action, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.prefixState) == 0 {
		var matched bool
		for action, pbs := range r.prefixes {
			for _, pb := range pbs {
				if len(pb.Chords) > 0 && chordMatches(msg, pb.Chords[0]) {
					if r.prefixMatches == nil {
						r.prefixMatches = map[Action]bool{}
					}
					r.prefixMatches[action] = true
					matched = true
				}
			}
		}
		if matched {
			r.prefixState = append(r.prefixState, chordString(msg))
		}
		return "", matched
	}

	r.prefixState = append(r.prefixState, chordString(msg))
	depth := len(r.prefixState)

	var completed Action
	var anyContinue bool
	for action, pbs := range r.prefixes {
		if !r.prefixMatches[action] {
			continue
		}
		var stillViable bool
		for _, pb := range pbs {
			if depth <= len(pb.Chords) && r.stateMatches(pb.Chords[:depth]) {
				stillViable = true
				if depth == len(pb.Chords) {
					completed = action
				}
			}
		}
		if !stillViable {
			delete(r.prefixMatches, action)
		} else {
			anyContinue = true
		}
	}

	if completed != "" {
		r.resetPrefix()
		return completed, true
	}
	if !anyContinue {
		r.resetPrefix()
	}
	return "", anyContinue
}

func (r *Registry) resetPrefix() {
	r.prefixState = nil
	r.prefixMatches = nil
}

// ResetPrefix forces the prefix state machine back to idle.
// Call when a modal opens or a non-prefix key arrives that shouldn't
// be interpreted as a prefix continuation.
func (r *Registry) ResetPrefix() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetPrefix()
}

// ChordOption is one possible next chord while a prefix is pending, with
// the action it would complete — feeds the chord-hint UI.
type ChordOption struct {
	Key  string // next chord, e.g. "ctrl+b", "m"
	Desc string // human-readable action description
}

// PendingChord is the active multi-chord prefix and the next chords that
// would complete an action from here.
type PendingChord struct {
	Prefix  string // chords pressed so far, space-joined (e.g. "ctrl+x")
	Options []ChordOption
}

// PendingPrefix reports the active prefix and its next-chord options.
// ok=false when no prefix is primed, OR when a prefix is primed but no
// next-chord options resolve (len(Options)==0) — callers use ok to decide
// whether to draw the hint, and an empty option list has nothing to show.
// Query at render time to show the chord hint; the prefix state lives in
// the registry and clears itself when the sequence completes or resets.
func (r *Registry) PendingPrefix() (PendingChord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	depth := len(r.prefixState)
	if depth == 0 {
		return PendingChord{}, false
	}
	pc := PendingChord{Prefix: strings.Join(r.prefixState, " ")}
	seen := map[string]bool{}
	for action := range r.prefixMatches {
		for _, pb := range r.prefixes[action] {
			if depth < len(pb.Chords) && r.stateMatches(pb.Chords[:depth]) {
				next := pb.Chords[depth]
				if seen[next] {
					continue
				}
				seen[next] = true
				pc.Options = append(pc.Options, ChordOption{Key: next, Desc: pb.Description})
			}
		}
	}
	sort.Slice(pc.Options, func(i, j int) bool { return pc.Options[i].Key < pc.Options[j].Key })
	return pc, len(pc.Options) > 0
}

// stateMatches reports whether the collected prefixState equals the
// given target slice (same length, same strings).
func (r *Registry) stateMatches(target []string) bool {
	if len(r.prefixState) != len(target) {
		return false
	}
	for i, s := range r.prefixState {
		if s != target[i] {
			return false
		}
	}
	return true
}

// chordMatches checks whether a single bubbletea KeyMsg matches a
// chord string like "ctrl+b".  Uses msg.String() which is exactly
// what bubbles/key.Binding compares against internally.
func chordMatches(msg tea.KeyPressMsg, chord string) bool {
	return msg.String() == chord
}

// chordString converts a bubbletea KeyMsg to a canonical chord string
// for prefix-state comparison.  msg.String() already returns the
// bubbles-compatible representation (e.g. "ctrl+b", "ctrl+x").
func chordString(msg tea.KeyPressMsg) string {
	return msg.String()
}

// ActionsByGroup returns bindings grouped for the help overlay
func (r *Registry) ActionsByGroup() map[string][]Action {
	return map[string][]Action{
		"App": {
			AppExit, TipsToggle, CommandList,
		},
		"Session": {
			SessionInterrupt, AgentSwitch, ModelSwitch, SessionSwitch, SessionNew, TreeView, Approve, Deny, EditSummary,
		},
		"View": {
			SidebarToggle, SidebarNarrower, SidebarWider, ThinkingToggle,
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
		"Modes": {
			ModeToggle, ModeToggleBtw,
		},
		"In-turn routing": {
			QueueMessage, InterruptTurn,
		},
	}
}
