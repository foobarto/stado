package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui/providerpicker"
	"github.com/zalando/go-keyring"
)

// providerModalModel builds a scenario model with an isolated, writable
// config path so the apply path writes to a temp file rather than the
// operator's real config.
func providerModalModel(t *testing.T) *Model {
	t.Helper()
	m := scenarioModel(t)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	m.cfg = &config.Config{ConfigPath: cfgPath}
	return m
}

// TestProviderSlashOpensModal: bare `/provider` opens the credential modal
// listing every known provider with a REDACTED status; `/provider <name>`
// keeps the setup-help text path (no modal).
func TestProviderSlashOpensModal(t *testing.T) {
	keyring.MockInit()
	m := providerModalModel(t)

	_ = m.handleSlash("/provider")
	if m.providerPick == nil || !m.providerPick.Visible {
		t.Fatal("/provider should open the provider picker")
	}
	if len(m.providerPick.Items) < len(config.KnownProviders()) {
		t.Fatalf("picker items = %d, want >= %d", len(m.providerPick.Items), len(config.KnownProviders()))
	}

	// Render must show provider names + env-var labels, never a secret.
	out := m.providerPick.View(120, 40)
	if !strings.Contains(out, "anthropic") || !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Errorf("modal missing anthropic / its env var:\n%s", out)
	}

	// `/provider anthropic` (with arg) takes the help-text branch, not the
	// modal — closing first so we can detect a re-open.
	m.providerPick.Close()
	_ = m.handleSlash("/provider anthropic")
	if m.providerPick.Visible {
		t.Error("/provider <name> should not open the modal")
	}
}

// TestProviderModalStatusRedactedReflectsEnv: a provider whose env var is
// exported shows "set" with the env-var NAME — never the secret value.
func TestProviderModalStatusRedactedReflectsEnv(t *testing.T) {
	keyring.MockInit() // empty in-memory keyring → env fallback resolves
	const secret = "sk-test-do-not-leak-XYZ"
	t.Setenv("DEEPSEEK_API_KEY", secret)

	m := providerModalModel(t)
	_ = m.handleSlash("/provider")
	out := m.providerPick.View(120, 40)

	if strings.Contains(out, secret) {
		t.Fatalf("secret value leaked into the modal:\n%s", out)
	}
	// deepseek should report configured (env source) with its env-var NAME.
	var found bool
	for _, it := range m.providerPick.Items {
		if it.Provider == "deepseek" {
			found = true
			if !it.Configured {
				t.Error("deepseek should be Configured when its env var is set")
			}
			if it.Source != "env" {
				t.Errorf("deepseek Source = %q, want env", it.Source)
			}
		}
	}
	if !found {
		t.Fatal("deepseek row missing from picker items")
	}
}

// TestApplyProviderSaveWritesRefNoSecretInBlocks: applying a Save command
// records the credential ref in config.toml (env-var NAME only) and the
// resulting system block never contains the secret.
func TestApplyProviderSaveWritesRefNoSecretInBlocks(t *testing.T) {
	keyring.MockInit() // in-memory keyring so the secret never hits the real one
	m := providerModalModel(t)
	_ = m.openProviderPicker()

	const secret = "sk-secret-should-not-appear"
	err := m.applyProviderCommand(providerpicker.Command{
		Type:     providerpicker.CommandSave,
		Provider: "deepseek",
		EnvVar:   "MY_DEEPSEEK_KEY",
		Secret:   secret,
	})
	if err != nil {
		t.Fatalf("applyProviderCommand: %v", err)
	}

	// The config file should now record the env-var NAME — and NOT the secret.
	data, rerr := os.ReadFile(m.cfg.ConfigPath)
	if rerr != nil {
		t.Fatalf("read config: %v", rerr)
	}
	cfgText := string(data)
	if !strings.Contains(cfgText, "MY_DEEPSEEK_KEY") {
		t.Errorf("config should record the env-var name:\n%s", cfgText)
	}
	if strings.Contains(cfgText, secret) {
		t.Fatalf("SECRET LEAKED INTO CONFIG:\n%s", cfgText)
	}

	// No rendered system block may contain the secret.
	for _, b := range m.blocks {
		if strings.Contains(b.body, secret) {
			t.Fatalf("secret leaked into a system block: %q", b.body)
		}
	}
	// A confirmation block citing the env-var NAME should exist.
	var confirmed bool
	for _, b := range m.blocks {
		if b.kind == "system" && strings.Contains(b.body, "MY_DEEPSEEK_KEY") {
			confirmed = true
		}
	}
	if !confirmed {
		t.Error("expected a redacted confirmation block naming the env var")
	}

	// The secret was persisted to the (mock) keyring under the env-var NAME
	// — proving the keyring write path runs, while staying off the real one.
	if got, gerr := keyring.Get("stado", "MY_DEEPSEEK_KEY"); gerr != nil || got != secret {
		t.Errorf("keyring should hold the secret under the env var: got=%q err=%v", got, gerr)
	}
}

// TestApplyProviderRemoveClearsRef: a Remove command deletes the preset
// block previously written for a provider.
func TestApplyProviderRemoveClearsRef(t *testing.T) {
	keyring.MockInit()
	m := providerModalModel(t)
	_ = m.openProviderPicker()

	// Seed a ref first.
	if err := m.applyProviderCommand(providerpicker.Command{
		Type:     providerpicker.CommandSave,
		Provider: "deepseek",
		EnvVar:   "MY_DEEPSEEK_KEY",
	}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	beforeRaw, _ := os.ReadFile(m.cfg.ConfigPath)
	if !strings.Contains(string(beforeRaw), "MY_DEEPSEEK_KEY") {
		t.Fatalf("precondition: ref not written:\n%s", beforeRaw)
	}

	if err := m.applyProviderCommand(providerpicker.Command{
		Type:     providerpicker.CommandRemove,
		Provider: "deepseek",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	afterRaw, _ := os.ReadFile(m.cfg.ConfigPath)
	if strings.Contains(string(afterRaw), "MY_DEEPSEEK_KEY") {
		t.Errorf("remove should clear the preset block:\n%s", afterRaw)
	}
	var removedBlock bool
	for _, b := range m.blocks {
		if b.kind == "system" && strings.Contains(b.body, "removed credential ref") {
			removedBlock = true
		}
	}
	if !removedBlock {
		t.Error("expected a removal confirmation block")
	}
}
