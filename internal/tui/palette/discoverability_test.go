package palette

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// matchNames returns the current match list as a set of command names.
func matchNames(m *Model) map[string]bool {
	out := make(map[string]bool, len(m.Matches))
	for _, c := range m.Matches {
		out[c.Name] = true
	}
	return out
}

func commandNames() map[string]bool {
	out := make(map[string]bool, len(Commands))
	for _, c := range Commands {
		out[c.Name] = true
	}
	return out
}

// TestCommands_IncludeWorkingDispatchedCommands pins the P2 defect:
// nine commands dispatch fine in model_commands.go (and are reserved in
// aliases.go) but were absent from palette.Commands, so they were
// invisible in /help, Ctrl+P, and the inline "/" popup. Every working
// slash command that an operator can run must be discoverable here —
// this list is the single source of truth the discovery surfaces read.
func TestCommands_IncludeWorkingDispatchedCommands(t *testing.T) {
	have := commandNames()
	// The nine that dispatch in handleSlash but were missing from the palette.
	want := []string{
		"/stats", "/ps", "/config", "/sandbox", "/fleet",
		"/kill", "/spawn", "/cancel", "/supervisor",
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("palette.Commands missing working command %q — it dispatches but is invisible in /help, Ctrl+P, and the inline popup", name)
		}
	}
}

// TestCommands_EveryEntryHasDescription guards against adding a bare
// command row with no description (the discovery surfaces render the
// Desc as the primary label).
func TestCommands_EveryEntryHasDescription(t *testing.T) {
	for _, c := range Commands {
		if strings.TrimSpace(c.Desc) == "" {
			t.Errorf("command %q has an empty description", c.Name)
		}
		if strings.TrimSpace(c.Group) == "" {
			t.Errorf("command %q has an empty group", c.Name)
		}
	}
}

// typeQuery drives the model the way a real keystroke stream would:
// open, then feed the query one rune at a time so refresh() runs.
func typeQuery(t *testing.T, q string) *Model {
	t.Helper()
	m := New()
	m.Open()
	for _, r := range q {
		_, _ = m.Update(tea.KeyPressMsg{Text: string(r)})
	}
	return m
}

// TestMatch_PrefixOfferExactCommand pins the P1 defect: a prefix query
// that is itself shorter than a command must offer EVERY command that
// has it as a prefix — typing "stat" must surface BOTH /status AND
// /stats, and typing the full name "ps" must surface /ps. The old
// fuzzy-only match collapsed a real command behind a different one
// (/stat → only /status; /ps subsequence-matched /persona and hid the
// real /ps), confusing two genuinely distinct commands.
func TestMatch_PrefixOfferExactCommand(t *testing.T) {
	cases := []struct {
		query string
		want  []string // commands that MUST appear in the match list
	}{
		// "stat" is a prefix of both /status and /stats — neither may hide.
		{"stat", []string{"/status", "/stats"}},
		{"/stat", []string{"/status", "/stats"}},
		// "ps" is itself a command name; it must be offered (and ranked
		// at/near the top), not buried behind a fuzzy /persona match.
		{"ps", []string{"/ps"}},
		{"/ps", []string{"/ps"}},
		// An exact full name must always be present.
		{"stats", []string{"/stats"}},
		{"status", []string{"/status"}},
	}
	for _, tc := range cases {
		m := typeQuery(t, tc.query)
		got := matchNames(m)
		for _, w := range tc.want {
			if !got[w] {
				var names []string
				for _, c := range m.Matches {
					names = append(names, c.Name)
				}
				t.Errorf("query %q: expected %q in matches, got %v", tc.query, w, names)
			}
		}
	}
}

// TestMatch_ExactNameRanksFirst: when the query is exactly a command
// name (or an exact prefix that is itself a command), that command must
// be the top-ranked match so Enter/Tab lands on the right command. "ps"
// must rank /ps first, not /persona.
func TestMatch_ExactNameRanksFirst(t *testing.T) {
	cases := []struct {
		query string
		top   string
	}{
		{"ps", "/ps"},
		{"stats", "/stats"},
		{"status", "/status"},
		{"help", "/help"},
	}
	for _, tc := range cases {
		m := typeQuery(t, tc.query)
		if len(m.Matches) == 0 {
			t.Errorf("query %q: no matches", tc.query)
			continue
		}
		if m.Matches[0].Name != tc.top {
			t.Errorf("query %q: top match = %q, want %q (full list: %v)",
				tc.query, m.Matches[0].Name, tc.top, names(m.Matches))
		}
	}
}

func names(cmds []Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name
	}
	return out
}

// TestInlinePopup_OffersExactCommand renders the actual inline "/" popup
// bytes and asserts the right command appears — reproduce-by-render, not
// by theorizing on the match list. Typing "/stat" must show /stats in
// the popup, and "/ps" must show /ps.
func TestInlinePopup_OffersExactCommand(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"stat", "/stats"},
		{"ps", "/ps"},
	}
	for _, tc := range cases {
		m := typeQuery(t, tc.query)
		out := m.InlineView(80)
		if !strings.Contains(out, tc.want) {
			t.Errorf("inline popup for %q missing %q:\n%s", tc.query, tc.want, out)
		}
	}
}
