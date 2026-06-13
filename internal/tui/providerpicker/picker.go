// Package providerpicker is the modal "/provider" picker: a popup that
// lists every known provider with its REDACTED credential status (the
// env-var NAME + a configured/unset marker, never the secret) and lets
// the operator add / modify / remove a provider credential from inside
// the TUI — the in-app counterpart to the `stado auth {list,set,unset}`
// CLI.
//
// It follows the taskpicker idiom: a list mode and a form sub-mode, with
// Update returning a Command the model layer applies (it owns config +
// keyring writes; the picker never touches disk). The form's secret
// field is MASKED — the entered key is held in formKey for the Command
// but rendered as dots, so a secret never reaches scrollback or the
// rendered modal. Esc is layered: form -> list -> close.
package providerpicker

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

const (
	maxQueryBytes  = 1024
	maxEnvBytes    = 256
	maxURLBytes    = 1024
	maxSecretBytes = 4096
)

// CommandType is the action the picker is asking the model layer to take.
type CommandType int

const (
	// CommandNone — no action (navigation / cancel).
	CommandNone CommandType = iota
	// CommandSave — write the credential ref (env-var NAME + optional
	// base_url) and, when Secret is non-empty and a keyring is available,
	// store the secret.
	CommandSave
	// CommandRemove — unset the provider's credential ref (+ any keyring
	// secret).
	CommandRemove
)

// Command is the picker's request to the model layer. Provider is the
// canonical provider name. EnvVar is the env-var NAME to record (never a
// secret). BaseURL is the optional override (only meaningful for kinds
// that allow it). Secret is the freshly-entered API key for a keyring
// write — it is REDACTED-by-construction in the picker (never rendered)
// and must be zeroed by the consumer after use.
type Command struct {
	Type     CommandType
	Provider string
	EnvVar   string
	BaseURL  string
	Secret   string
}

// Item is one provider row, REDACTED. The model layer builds these from
// config.ProviderCredentialStatus so the resolution logic matches the
// runtime; the picker only displays them.
type Item struct {
	// Provider is the canonical provider name.
	Provider string
	// Kind is the human-readable provider kind ("native",
	// "oai-compat-cloud", ...).
	Kind string
	// EnvVar is the env-var NAME stado reads for this provider's secret.
	// Empty for local runners that need no key.
	EnvVar string
	// BaseURL is any configured base-URL override (non-secret).
	BaseURL string
	// Configured is true when a credential resolves right now (or the
	// provider needs no key).
	Configured bool
	// Source names where the secret resolved from ("env", "os-keyring"),
	// or "" when unconfigured. Never the secret.
	Source string
	// NeedsKey is false for local runners (no key expected).
	NeedsKey bool
	// AllowsBaseURL is true for the kinds that accept a base-URL override
	// (anthropic-compat-cloud + OAI-compat). The form enables / disables
	// the base_url field on this.
	AllowsBaseURL bool
}

type mode int

const (
	modeList mode = iota
	modeForm
	modeRemove
)

// form field indices.
const (
	fieldEnv = iota
	fieldBaseURL
	fieldKey
	fieldCount
)

// Model is the modal picker. Open populates Items; Update drives the
// keypress loop while Visible is true.
type Model struct {
	Visible bool
	Query   string
	Items   []Item
	Matches []Item
	Cursor  int
	Notice  string

	// keyringAvailable gates the masked secret field: when false the
	// form shows the env-export hint instead of a key-entry row, mirroring
	// the CLI's ENV-FIRST fallback.
	keyringAvailable bool

	mode      mode
	target    Item
	formEnv   string
	formURL   string
	formKey   string // MASKED in the view; carried into the Save command only.
	formField int

	// Outer screen size so View can centre the modal.
	Width  int
	Height int
}

func New() *Model { return &Model{} }

// Open shows the picker seeded with items. keyringAvailable reflects
// config.SecretBackendAvailable() — it decides whether the form offers a
// masked key-entry field or the env-export hint. selected is the provider
// to land the cursor on (empty = first).
func (m *Model) Open(items []Item, keyringAvailable bool, selected string) {
	m.Visible = true
	m.Query = ""
	m.Notice = ""
	m.mode = modeList
	m.target = Item{}
	m.keyringAvailable = keyringAvailable
	m.Items = append([]Item(nil), items...)
	m.refresh()
	m.Cursor = 0
	if selected != "" {
		m.selectProvider(selected)
	}
}

// Close dismisses the picker without a selection.
func (m *Model) Close() { m.Visible = false }

// SetNotice surfaces a one-line message (error / confirmation) at the top
// of the modal until the next keypress.
func (m *Model) SetNotice(text string) { m.Notice = strings.TrimSpace(text) }

// Selected returns the highlighted item, or nil when hidden / empty.
func (m *Model) Selected() *Item {
	if !m.Visible || len(m.Matches) == 0 {
		return nil
	}
	return &m.Matches[m.Cursor]
}

// Update consumes a keypress while Visible. handled=true means the caller
// MUST NOT forward the key to the underlying text input.
func (m *Model) Update(msg tea.Msg) (Command, bool) {
	if !m.Visible {
		return Command{}, false
	}
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return Command{}, false
	}
	m.Notice = ""
	switch m.mode {
	case modeForm:
		return m.updateForm(km), true
	case modeRemove:
		return m.updateRemove(km), true
	default:
		return m.updateList(km), true
	}
}

func (m *Model) updateList(km tea.KeyPressMsg) Command {
	switch km.String() {
	case "up":
		m.moveCursor(-1)
	case "down", "tab":
		m.moveCursor(1)
	case "enter":
		if sel := m.Selected(); sel != nil {
			m.beginEdit(*sel)
		}
	case "esc":
		m.Close()
	case "ctrl+d":
		if sel := m.Selected(); sel != nil && sel.NeedsKey {
			m.beginRemove(*sel)
		}
	case "backspace":
		if len(m.Query) > 0 {
			m.Query = textutil.TrimLastRune(m.Query)
			m.refresh()
		}
	case "ctrl+u":
		m.Query = ""
		m.refresh()
	case "space":
		m.Query = textutil.AppendWithinBytes(m.Query, " ", maxQueryBytes)
		m.refresh()
	default:
		if km.Text != "" {
			m.Query = textutil.AppendWithinBytes(m.Query, km.Text, maxQueryBytes)
			m.refresh()
		}
	}
	return Command{}
}

func (m *Model) updateForm(km tea.KeyPressMsg) Command {
	switch km.String() {
	case "esc":
		// Layered Esc: form -> list.
		m.mode = modeList
	case "enter":
		return Command{
			Type:     CommandSave,
			Provider: m.target.Provider,
			EnvVar:   strings.TrimSpace(m.formEnv),
			BaseURL:  strings.TrimSpace(m.formURL),
			Secret:   m.formKey,
		}
	case "up":
		m.formField = m.prevField()
	case "down", "tab":
		m.formField = m.nextField()
	case "backspace":
		switch m.formField {
		case fieldEnv:
			m.formEnv = textutil.TrimLastRune(m.formEnv)
		case fieldBaseURL:
			if m.target.AllowsBaseURL {
				m.formURL = textutil.TrimLastRune(m.formURL)
			}
		case fieldKey:
			if m.keyEntryEnabled() {
				m.formKey = textutil.TrimLastRune(m.formKey)
			}
		}
	case "ctrl+u":
		switch m.formField {
		case fieldEnv:
			m.formEnv = ""
		case fieldBaseURL:
			if m.target.AllowsBaseURL {
				m.formURL = ""
			}
		case fieldKey:
			if m.keyEntryEnabled() {
				m.formKey = ""
			}
		}
	case "space":
		// Spaces aren't valid in env-var names / URLs / keys; ignore so
		// the field can't accumulate junk.
	default:
		if km.Text != "" {
			m.appendToField(km.Text)
		}
	}
	return Command{}
}

func (m *Model) updateRemove(km tea.KeyPressMsg) Command {
	switch km.String() {
	case "enter":
		return Command{Type: CommandRemove, Provider: m.target.Provider}
	case "esc":
		m.mode = modeList
	default:
		switch km.Text {
		case "y", "Y":
			return Command{Type: CommandRemove, Provider: m.target.Provider}
		case "n", "N":
			m.mode = modeList
		}
	}
	return Command{}
}

// keyEntryEnabled reports whether the masked key field is active: the
// provider needs a key AND a keyring is available to persist it. Without
// a keyring the form shows the env-export hint instead (ENV-FIRST).
func (m *Model) keyEntryEnabled() bool {
	return m.target.NeedsKey && m.keyringAvailable
}

// nextField / prevField skip the fields that aren't active for this
// provider (base_url when the kind disallows it; the key field when there's
// no key or no keyring) so tab never lands on an uneditable row.
func (m *Model) nextField() int {
	f := m.formField
	for i := 0; i < fieldCount; i++ {
		f = (f + 1) % fieldCount
		if m.fieldEditable(f) {
			return f
		}
	}
	return m.formField
}

func (m *Model) prevField() int {
	f := m.formField
	for i := 0; i < fieldCount; i++ {
		f = (f + fieldCount - 1) % fieldCount
		if m.fieldEditable(f) {
			return f
		}
	}
	return m.formField
}

func (m *Model) fieldEditable(f int) bool {
	switch f {
	case fieldEnv:
		return true
	case fieldBaseURL:
		return m.target.AllowsBaseURL
	case fieldKey:
		return m.keyEntryEnabled()
	}
	return false
}

func (m *Model) appendToField(s string) {
	switch m.formField {
	case fieldEnv:
		m.formEnv = textutil.AppendWithinBytes(m.formEnv, s, maxEnvBytes)
	case fieldBaseURL:
		if m.target.AllowsBaseURL {
			m.formURL = textutil.AppendWithinBytes(m.formURL, s, maxURLBytes)
		}
	case fieldKey:
		if m.keyEntryEnabled() {
			m.formKey = textutil.AppendWithinBytes(m.formKey, s, maxSecretBytes)
		}
	}
}

func (m *Model) beginEdit(it Item) {
	if it.Provider == "" {
		return
	}
	m.mode = modeForm
	m.target = it
	m.formEnv = it.EnvVar
	m.formURL = it.BaseURL
	m.formKey = ""
	// Land on the first editable field.
	m.formField = fieldEnv
	if !m.fieldEditable(fieldEnv) {
		m.formField = m.nextField()
	}
}

func (m *Model) beginRemove(it Item) {
	if it.Provider == "" {
		return
	}
	m.mode = modeRemove
	m.target = it
}

func (m *Model) moveCursor(delta int) {
	if len(m.Matches) == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor = (m.Cursor + delta + len(m.Matches)) % len(m.Matches)
}

func (m *Model) refresh() {
	q := strings.TrimSpace(m.Query)
	if q == "" {
		m.Matches = append([]Item(nil), m.Items...)
	} else {
		words := make([]string, len(m.Items))
		for i, it := range m.Items {
			words[i] = it.Provider + " " + it.Kind + " " + it.EnvVar
		}
		found := fuzzy.Find(q, words)
		m.Matches = nil
		for _, f := range found {
			m.Matches = append(m.Matches, m.Items[f.Index])
		}
	}
	if m.Cursor >= len(m.Matches) {
		m.Cursor = len(m.Matches) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

func (m *Model) selectProvider(provider string) bool {
	for i, it := range m.Matches {
		if it.Provider == provider {
			m.Cursor = i
			return true
		}
	}
	return false
}

// View renders the modal centred on the provided canvas size.
func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	// Half-screen width, clamped to a usable [58,98] band, then capped so the
	// centred box keeps a 2-col margin on each side at narrow terminals. Without
	// the cap a <=60-col terminal pins modalW at its 58 floor (border included),
	// leaving only a 1-col margin once Place centres it — touching the edge and
	// risking clipping. The cap wins over the 58 floor on purpose: a modal that
	// fits with breathing room beats one that fills the screen.
	modalW := minInt(clampInt(screenWidth/2, 58, 98), maxInt(screenWidth-4, 1))
	body := m.renderBody(modalW - 4)
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Background(theme.Background).
		Padding(0, 1).
		Width(modalW).
		Render(body)
	return lipgloss.Place(screenWidth, screenHeight,
		lipgloss.Center, lipgloss.Center,
		modal)
}

func (m *Model) renderBody(innerW int) string {
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(theme.Background)

	titleText := "Provider credentials"
	hints := "enter edit  ctrl+d unset  esc"
	switch m.mode {
	case modeForm:
		titleText = "Edit " + m.target.Provider
		hints = "enter save  tab field  esc back"
	case modeRemove:
		titleText = "Unset " + m.target.Provider
		hints = "enter/y unset  esc/n cancel"
	}
	hintText := hints
	if lipgloss.Width(titleText)+lipgloss.Width(hintText)+1 > innerW {
		hintText = truncateVisible(hintText, maxInt(innerW-lipgloss.Width(titleText)-2, 8))
	}
	title := bg.Foreground(theme.Text).Bold(true).Render(titleText)
	hint := bg.Foreground(theme.Muted).Render(hintText)
	headerPad := maxInt(innerW-lipgloss.Width(titleText)-lipgloss.Width(hintText), 1)
	b.WriteString(title + bg.Render(strings.Repeat(" ", headerPad)) + hint)
	b.WriteString("\n\n")
	if m.Notice != "" {
		b.WriteString(bg.Foreground(theme.Error).Render(truncateVisible(m.Notice, innerW)))
		b.WriteString("\n\n")
	}

	switch m.mode {
	case modeForm:
		m.renderForm(&b, innerW)
	case modeRemove:
		m.renderRemove(&b, innerW)
	default:
		m.renderList(&b, innerW)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderList(b *strings.Builder, innerW int) {
	bg := lipgloss.NewStyle().Background(theme.Background)
	searchLabel := bg.Foreground(theme.Text).Render("Search")
	cursor := bg.Foreground(theme.Text).
		Background(theme.Primary).
		Render(" ")
	queryDisplay := bg.Foreground(theme.Text).Render(m.Query)
	if m.Query == "" {
		b.WriteString(searchLabel + cursor)
	} else {
		b.WriteString(queryDisplay + cursor)
	}
	b.WriteString("\n\n")

	if len(m.Matches) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).Render("no matches"))
		return
	}
	start := 0
	limit := 12
	if len(m.Matches) < limit {
		limit = len(m.Matches)
	}
	if m.Cursor >= limit {
		start = m.Cursor - limit + 1
	}
	for i, it := range m.Matches[start : start+limit] {
		idx := start + i
		isSel := idx == m.Cursor
		left := textutil.StripControlChars(it.Provider)
		right := credStatusLabel(it)
		if isSel {
			padded := rowTwoCol(innerW, left, right)
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(padded))
		} else {
			leftText := truncateVisible(left, maxInt(innerW-lipgloss.Width(right)-1, 3))
			pad := maxInt(innerW-lipgloss.Width(leftText)-lipgloss.Width(right), 1)
			tone := theme.Muted
			if it.NeedsKey && it.Configured {
				tone = theme.Success
			} else if it.NeedsKey && !it.Configured {
				tone = theme.Warning
			}
			b.WriteString(bg.Foreground(theme.Text).Render(leftText) +
				bg.Render(strings.Repeat(" ", pad)) +
				bg.Foreground(tone).Render(right))
		}
		b.WriteString("\n")
	}
	if hidden := len(m.Matches) - limit; hidden > 0 {
		b.WriteString(bg.Foreground(theme.Muted).
			Render("+" + itoa(hidden) + " more; keep typing to narrow"))
	}
}

// credStatusLabel is the REDACTED right-column status: a marker plus the
// env-var NAME (never the secret). Mirrors the CLI's `auth list`.
func credStatusLabel(it Item) string {
	if !it.NeedsKey {
		return "(no key needed)"
	}
	env := it.EnvVar
	if env == "" {
		env = "-"
	}
	if it.Configured {
		src := it.Source
		if src == "" {
			src = "env"
		}
		return "set " + env + " (" + src + ")"
	}
	return "unset " + env
}

func (m *Model) renderForm(b *strings.Builder, innerW int) {
	bg := lipgloss.NewStyle().Background(theme.Background)

	type row struct {
		label    string
		value    string
		editable bool
		hint     string
	}
	rows := []row{
		{"Env var", m.formEnv, true, "the NAME of the env var holding the key"},
	}
	if m.target.AllowsBaseURL {
		urlVal := m.formURL
		if urlVal == "" {
			urlVal = "(default endpoint)"
		}
		rows = append(rows, row{"Base URL", urlVal, true, "optional override"})
	} else if m.target.NeedsKey {
		rows = append(rows, row{"Base URL", "(not allowed for this kind)", false, ""})
	}
	if m.target.NeedsKey {
		if m.keyringAvailable {
			masked := maskSecret(m.formKey)
			if masked == "" {
				masked = "(leave blank to keep current)"
			}
			rows = append(rows, row{"API key", masked, true, "stored in OS keyring; never shown"})
		} else {
			rows = append(rows, row{"API key", "(no keyring — use export hint below)", false, ""})
		}
	}

	// Map visual rows back to field indices so the highlight tracks the
	// active field even when some fields are hidden.
	fieldForRow := func(label string) int {
		switch label {
		case "Env var":
			return fieldEnv
		case "Base URL":
			return fieldBaseURL
		case "API key":
			return fieldKey
		}
		return -1
	}

	for _, r := range rows {
		valDisplay := r.value
		if valDisplay == "" {
			valDisplay = " "
		}
		line := rowTwoCol(innerW, r.label, truncateVisible(valDisplay, maxInt(innerW-lipgloss.Width(r.label)-2, 8)))
		selected := r.editable && fieldForRow(r.label) == m.formField
		if selected {
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(line))
		} else if r.editable {
			b.WriteString(bg.Foreground(theme.Text).Render(line))
		} else {
			b.WriteString(bg.Foreground(theme.Muted).Render(line))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if !m.target.NeedsKey {
		b.WriteString(bg.Foreground(theme.Muted).
			Render(wrapPlain("This provider is a local runner and needs no API key. Saving records the endpoint reference only.", innerW)))
		return
	}
	if !m.keyringAvailable {
		env := strings.TrimSpace(m.formEnv)
		if env == "" {
			env = m.target.EnvVar
		}
		if env == "" {
			env = "<ENV_VAR>"
		}
		b.WriteString(bg.Foreground(theme.Muted).
			Render(wrapPlain("No OS keyring available; secrets are never written to config. Export the key in your shell instead:", innerW)))
		b.WriteString("\n")
		b.WriteString(bg.Foreground(theme.Text).Render("  export " + env + "=<your-key>"))
		return
	}
	b.WriteString(bg.Foreground(theme.Muted).
		Render(wrapPlain("Enter a key to store it in the OS keyring, or leave it blank to keep the current secret and just update the env-var name / base URL.", innerW)))
}

func (m *Model) renderRemove(b *strings.Builder, innerW int) {
	bg := lipgloss.NewStyle().Background(theme.Background)
	b.WriteString(bg.Foreground(theme.Error).Bold(true).
		Render("Unset credential for " + m.target.Provider + "?"))
	b.WriteString("\n")
	msg := "This removes the config-side reference"
	if m.target.NeedsKey && m.target.Source == "os-keyring" {
		msg += " and the stored keyring secret"
	}
	msg += ". Any exported environment variable is left untouched."
	b.WriteString(bg.Foreground(theme.Muted).Render(wrapPlain(msg, innerW)))
}

// maskSecret renders a secret as dots — its visible LENGTH, capped so a
// long key doesn't reveal an exact length and doesn't overflow the row.
// The actual bytes are never returned. Empty input yields "".
func maskSecret(s string) string {
	n := len([]rune(s))
	if n == 0 {
		return ""
	}
	if n > 24 {
		n = 24
	}
	return strings.Repeat("•", n)
}

// ---- layout helpers (kept local to avoid a cross-package TUI dep) ----

func rowTwoCol(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > width {
		budget := width - rw - 2
		if budget < 3 {
			budget = 3
		}
		left = truncateVisible(left, budget)
		lw = lipgloss.Width(left)
	}
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func wrapPlain(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := ""
	for _, word := range words {
		if line == "" {
			line = word
			continue
		}
		if lipgloss.Width(line)+1+lipgloss.Width(word) > width {
			b.WriteString(line)
			b.WriteString("\n")
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		b.WriteString(line)
	}
	return b.String()
}

// truncateVisible bounds s to width DISPLAY columns (incl. the ellipsis
// tail), via ansi.Truncate. A rune-count slice under-budgeted wide-CJK/emoji
// values (provider hints, secret-source labels, notices) — a string whose
// rune count fit width still had ~2x display width and hard-wrapped / overflowed
// the modal border. ansi.Truncate is display-width- and grapheme-aware.
func truncateVisible(s string, width int) string {
	if width <= 1 {
		return "…"
	}
	return ansi.Truncate(s, width, "…")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
