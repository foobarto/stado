package tui

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/localdetect"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/modelpicker"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func dialQuick(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 50*time.Millisecond)
}

func newPickerTestModel(t *testing.T, providerName string) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel("/tmp", "starter-model", providerName,
		func() (agent.Provider, error) { return nil, nil },
		rnd, reg)
	m.width, m.height = 120, 40
	return m
}

// TestOpenModelPicker_Anthropic: `/model` with no args opens the picker
// pre-selected on the current model (starter isn't in the catalog, so
// cursor falls back to 0).
func TestOpenModelPicker_Anthropic(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	if !m.modelPicker.Visible {
		t.Fatal("picker should be visible after openModelPicker()")
	}
	sel := m.modelPicker.Selected()
	if sel == nil {
		t.Fatal("picker has no selection")
	}
	// First catalog entry for anthropic.
	if sel.ID != "claude-opus-4-7" {
		t.Errorf("cursor at %q, expected catalog[0]=claude-opus-4-7", sel.ID)
	}
}

// TestOpenModelPicker_UnknownProviderAdvisory: no catalog AND no
// running local runners → system block, picker stays hidden.
//
// This test is skipped when the host has any bundled local runner up,
// because localdetect will populate the picker with the runner's
// live model list and the advisory path doesn't trigger. A CI runner
// shouldn't have those up, so the check exercises where it matters.
func TestOpenModelPicker_UnknownProviderAdvisory(t *testing.T) {
	// Fail-open: if localhost has a runner up, skip this test.
	if hasAnyLocalRunner(t) {
		t.Skip("host has a local runner running; advisory-path test assumes clean env")
	}
	m := newPickerTestModel(t, "some-custom-preset")
	m.openModelPicker()
	if m.modelPicker.Visible {
		t.Error("unknown provider should NOT open the picker")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected advisory block")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !contains(last.body, "no known models") {
		t.Errorf("expected 'no known models' advisory, got %+v", last)
	}
}

// hasAnyLocalRunner is a cheap sniff — dial each of the default local
// endpoints and return true if any accepts a TCP connection. A full
// localdetect probe would add 1+ seconds per case; we just want to
// know "is the environment clean" before running advisory-path tests.
func hasAnyLocalRunner(t *testing.T) bool {
	t.Helper()
	for _, port := range []string{"11434", "8080", "8000", "1234"} {
		conn, err := dialQuick("127.0.0.1:" + port)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// TestModelPickerSubmitAppliesSelection: drive the Update flow via a
// submit keypress and confirm m.model changed.
func TestModelPickerSubmitAppliesSelection(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	// Cursor starts on claude-opus-4-7. Down + Submit should land on
	// claude-opus-4-6.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Submit.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.modelPicker.Visible {
		t.Error("picker should have closed after submit")
	}
	if m.model != "claude-opus-4-6" {
		t.Errorf("m.model = %q, want claude-opus-4-6", m.model)
	}
	// Last block should announce the swap.
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !contains(last.body, "starter-model → claude-opus-4-6") {
		t.Errorf("announce missing: %+v", last)
	}
}

// TestModelPickerEscapeDismisses: Esc closes without mutating m.model.
func TestModelPickerEscapeDismisses(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.modelPicker.Visible {
		t.Error("Esc should have closed the picker")
	}
	if m.model != "starter-model" {
		t.Errorf("Esc should not change m.model, got %q", m.model)
	}
}

// TestHandleSlashModelWithArgStillWorks: the args form bypasses the
// picker and applies inline.
func TestHandleSlashModelWithArgStillWorks(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_ = m.handleSlash("/model claude-sonnet-4-5")
	if m.modelPicker.Visible {
		t.Error("picker should NOT open when /model has args")
	}
	if m.model != "claude-sonnet-4-5" {
		t.Errorf("m.model = %q, want claude-sonnet-4-5", m.model)
	}
}

// TestModelPickerSwitchesProviderOnDetectedPick is the regression
// against the "selecting lmstudio model but provider stays anthropic"
// user report. Seeding the picker with a detected-local item and
// submitting must swap m.providerName AND invalidate m.provider so
// the next ensureProvider rebuilds against the new backend.
func TestModelPickerSwitchesProviderOnDetectedPick(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")

	// Directly seed the picker — mimics what openModelPicker would
	// produce when lmstudio is reachable with one model loaded.
	m.modelPicker.Open([]modelpicker.Item{
		{
			ID:           "qwen/qwen3.6-35b-a3b",
			Origin:       "lmstudio · detected",
			ProviderName: "lmstudio",
		},
	}, m.model)

	// Enter — apply selection.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.model != "qwen/qwen3.6-35b-a3b" {
		t.Errorf("m.model = %q, want qwen/qwen3.6-35b-a3b", m.model)
	}
	if m.providerName != "lmstudio" {
		t.Errorf("m.providerName = %q, want lmstudio — provider didn't switch", m.providerName)
	}
	if m.provider != nil {
		t.Errorf("m.provider should have been nilled for rebuild")
	}

	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" ||
		!contains(last.body, "qwen/qwen3.6-35b-a3b") ||
		!contains(last.body, "anthropic → lmstudio") {
		t.Errorf("announcement missing model or provider swap: %+v", last)
	}
}

// TestModelPickerSameProviderNoSwitch: picking a catalog entry for the
// same provider the user's already on shouldn't announce a change.
func TestModelPickerSameProviderNoSwitch(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker() // catalog items tagged ProviderName="anthropic"
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerName != "anthropic" {
		t.Errorf("providerName should stay anthropic, got %q", m.providerName)
	}
	last := m.blocks[len(m.blocks)-1]
	if contains(last.body, "provider:") && contains(last.body, "→") {
		t.Errorf("same-provider pick should not announce a provider swap: %q", last.body)
	}
}

func TestModelPickerRemembersRecentSelection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	m := newPickerTestModel(t, "anthropic")
	m.cfg = cfg
	m.rememberModelSelection(modelpicker.Item{
		ID:           "claude-sonnet-4-5",
		Origin:       "anthropic",
		ProviderName: "anthropic",
	})

	recents := m.modelRecents()
	if len(recents) != 1 || recents[0].ID != "claude-sonnet-4-5" || !recents[0].Recent {
		t.Fatalf("recents not persisted/restored: %+v", recents)
	}

	m.model = "not-in-catalog"
	m.openModelPicker()
	sel := m.modelPicker.Selected()
	if sel == nil || sel.ID != "claude-sonnet-4-5" || !sel.Recent {
		t.Fatalf("recent model should be first selection, got %+v", sel)
	}
}

func TestModelPickerFavoritesPersistAndPrepend(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	m := newPickerTestModel(t, "anthropic")
	m.cfg = cfg
	favorite := m.toggleModelFavorite(modelpicker.Item{
		ID:           "claude-sonnet-4-5",
		Origin:       "anthropic",
		ProviderName: "anthropic",
	})
	if !favorite {
		t.Fatal("first toggle should add favorite")
	}
	favorites := m.modelFavorites()
	if len(favorites) != 1 || favorites[0].ID != "claude-sonnet-4-5" || !favorites[0].Favorite {
		t.Fatalf("favorites not persisted/restored: %+v", favorites)
	}

	m.model = "not-in-catalog"
	m.openModelPicker()
	sel := m.modelPicker.Selected()
	if sel == nil || sel.ID != "claude-sonnet-4-5" || !sel.Favorite {
		t.Fatalf("favorite model should be first selection, got %+v", sel)
	}
}

func TestModelPickerCtrlFTogglesFavorite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	m := newPickerTestModel(t, "anthropic")
	m.cfg = cfg
	m.openModelPicker()
	sel := m.modelPicker.Selected()
	if sel == nil {
		t.Fatal("picker should have a selected model")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	after := m.modelPicker.Selected()
	if after == nil || !after.Favorite {
		t.Fatalf("ctrl+f should mark selected model favorite, got %+v", after)
	}
	favorites := m.modelFavorites()
	if len(favorites) != 1 || favorites[0].ID != sel.ID {
		t.Fatalf("favorite not persisted: %+v", favorites)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	after = m.modelPicker.Selected()
	if after == nil || after.Favorite {
		t.Fatalf("second ctrl+f should unmark favorite, got %+v", after)
	}
	if favorites := m.modelFavorites(); len(favorites) != 0 {
		t.Fatalf("favorite not removed: %+v", favorites)
	}
}

func TestModelPickerCtrlAShowsProviderSetup(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	if !m.modelPicker.Visible {
		t.Fatal("picker should be visible")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})

	if m.modelPicker.Visible {
		t.Fatal("ctrl+a should close the picker after showing setup")
	}
	if m.model != "starter-model" {
		t.Fatalf("provider setup should not change model, got %q", m.model)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" ||
		!contains(last.body, "provider setup: anthropic") ||
		!contains(last.body, "ANTHROPIC_API_KEY") {
		t.Fatalf("setup block missing provider credential hint: %+v", last)
	}
}

func TestProviderSetupBodyLocalRunner(t *testing.T) {
	m := newPickerTestModel(t, "lmstudio")
	body := m.providerSetupBody("lmstudio")
	for _, want := range []string{
		"provider setup: lmstudio",
		"bundled endpoint: http://localhost:1234/v1",
		"LM Studio local server",
		"lms load <model>",
	} {
		if !contains(body, want) {
			t.Fatalf("provider setup missing %q:\n%s", want, body)
		}
	}
}

func TestProvidersOverviewShowsNoModelRemediation(t *testing.T) {
	m := newPickerTestModel(t, "lmstudio")
	got := m.renderProvidersOverviewFromResults([]localdetect.Result{{
		Name:      "lmstudio",
		Endpoint:  "http://localhost:1234/v1",
		Reachable: true,
	}})
	for _, want := range []string{
		"running · no models loaded",
		"lms load <model>",
	} {
		if !contains(got, want) {
			t.Fatalf("providers overview missing %q:\n%s", want, got)
		}
	}
}

func TestProvidersOverviewShowsCredentialHealth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	m := newPickerTestModel(t, "openai")
	got := m.renderProvidersOverviewFromResults(nil)
	for _, want := range []string{
		"active provider: openai",
		"credentials: missing OPENAI_API_KEY",
		"/model Ctrl+A",
	} {
		if !contains(got, want) {
			t.Fatalf("providers overview missing %q:\n%s", want, got)
		}
	}
}

func TestProviderSetupBodyConfiguredPreset(t *testing.T) {
	m := newPickerTestModel(t, "custom")
	m.cfg = &config.Config{
		Inference: config.Inference{
			Presets: map[string]config.InferencePreset{
				"custom": {Endpoint: "http://localhost:9999/v1"},
			},
		},
	}
	body := m.providerSetupBody("custom")
	if !contains(body, "configured preset endpoint: http://localhost:9999/v1") {
		t.Fatalf("provider setup should prefer configured preset:\n%s", body)
	}
}

func TestModelPickerSelectionPersistsDefaultModel(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	m.cfg = &config.Config{ConfigPath: cfgPath}
	m.openModelPicker()
	sel := m.modelPicker.Selected()
	if sel == nil {
		t.Fatal("model picker has no selection")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	wantProvider := sel.ProviderName
	if wantProvider == "" {
		wantProvider = "anthropic"
	}
	for _, want := range []string{`provider = "` + wantProvider + `"`, `model = "` + sel.ID + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("persisted defaults missing %q:\n%s", want, body)
		}
	}
}

func TestModelSlashPersistsDefaultModel(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	m.cfg = &config.Config{ConfigPath: cfgPath}

	_ = m.handleSlash("/model claude-sonnet-4-6")

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `model = "claude-sonnet-4-6"`) {
		t.Fatalf("persisted defaults missing model:\n%s", body)
	}
}

func TestModelPickerShortcutOpensPicker(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})

	if !m.modelPicker.Visible {
		t.Fatal("ctrl+x m should open model picker")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
