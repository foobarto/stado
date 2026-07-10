package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/runtime"
)

func TestVerifySlashTogglesConfiguredGate(t *testing.T) {
	m := scenarioModel(t)
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 2}

	m.handleSlash("/verify on")
	if !m.verifyEnabled || !strings.Contains(m.blocks[len(m.blocks)-1].body, "verify: on") {
		t.Fatalf("/verify on state=%v block=%q", m.verifyEnabled, m.blocks[len(m.blocks)-1].body)
	}
	m.handleSlash("/verify")
	if m.verifyEnabled || !strings.Contains(m.blocks[len(m.blocks)-1].body, "verify: off") {
		t.Fatalf("/verify toggle state=%v block=%q", m.verifyEnabled, m.blocks[len(m.blocks)-1].body)
	}
}

func TestVerifySlashOffCancelsAndIgnoresActiveGateResult(t *testing.T) {
	m := scenarioModel(t)
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"slow-check"}, MaxRounds: 2}
	m.verifyEnabled = true
	m.verifying = true
	m.state = stateStreaming
	cancelled := false
	m.toolCancel = func() { cancelled = true }

	m.handleSlash("/verify off")
	if m.verifyEnabled || !cancelled {
		t.Fatalf("/verify off enabled=%v cancelled=%v", m.verifyEnabled, cancelled)
	}
	model, cmd := onVerifyResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyFailed, Round: 1, Output: "late failure", Feedback: "retry me",
	}})
	got := model.(*Model)
	if cmd != nil || got.state != stateIdle || got.verifying {
		t.Fatalf("disabled result state=%v verifying=%v cmd=%v", got.state, got.verifying, cmd != nil)
	}
	if len(got.msgs) != 0 {
		t.Fatalf("disabled result appended verifier feedback: %+v", got.msgs)
	}
}

func TestConfigReloadRefreshesVerifyGate(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	dir := filepath.Join(configHome, "stado")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`[verify]
commands = ["go test ./..."]
max_rounds = 4
strict = true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	m := scenarioModel(t)
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"old"}, MaxRounds: 1}
	m.verifyEnabled = false
	m.verifyRounds = 1
	m.handleConfigReload()

	if !m.verifyEnabled || len(m.verifyConfig.Commands) != 1 || m.verifyConfig.Commands[0] != "go test ./..." {
		t.Fatalf("reloaded verify config = %+v enabled=%v", m.verifyConfig, m.verifyEnabled)
	}
	if m.verifyConfig.MaxRounds != 4 || !m.verifyConfig.Strict || m.verifyRounds != 0 {
		t.Fatalf("reloaded verify state = %+v rounds=%d", m.verifyConfig, m.verifyRounds)
	}
}

func TestVerifyResultPassFinishesTurn(t *testing.T) {
	m := scenarioModel(t)
	m.verifyEnabled = true
	m.verifying = true
	m.state = stateStreaming
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"true"}, MaxRounds: 2}

	model, cmd := onVerifyResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyPassed, Round: 1,
	}})
	got := model.(*Model)
	if cmd != nil {
		t.Fatal("pass with no queued prompt should not return a command")
	}
	if got.state != stateIdle || got.verifying {
		t.Fatalf("pass state=%v verifying=%v", got.state, got.verifying)
	}
	if body := got.blocks[len(got.blocks)-1].body; !strings.Contains(body, "verification passed") {
		t.Fatalf("pass block = %q", body)
	}
}

func TestVerifyResultExhaustionIsDistinctAndRestoresQueuedInput(t *testing.T) {
	m := scenarioModel(t)
	m.verifyEnabled = true
	m.verifying = true
	m.state = stateStreaming
	m.queuedPrompt = "operator follow-up"
	m.blocks = append(m.blocks, block{kind: "user", body: m.queuedPrompt, queued: true})
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"false"}, MaxRounds: 2}
	m.loop = &loopState{prompt: "repeat"}

	model, _ := onVerifyResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyFailed, Round: 2, Output: "command exited with code 1", Feedback: "failed",
	}})
	got := model.(*Model)
	if got.state != stateError || !strings.Contains(got.errorMsg, "verify_exhausted") {
		t.Fatalf("exhausted state=%v error=%q", got.state, got.errorMsg)
	}
	if got.input.Value() != "operator follow-up" || got.queuedPrompt != "" {
		t.Fatalf("queued input not restored: input=%q queue=%q", got.input.Value(), got.queuedPrompt)
	}
	if got.loop != nil {
		t.Fatal("exhausted verification left a dead loop active")
	}
}

func TestEnterDuringVerificationQueuesAndSurvivesExhaustion(t *testing.T) {
	m := scenarioModel(t)
	m.verifyEnabled = true
	m.verifying = true
	m.state = stateStreaming
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"false"}, MaxRounds: 1}
	m.input.SetValue("do not lose this")

	model, _, handled := submitInput(m)
	got := model.(*Model)
	if !handled || got.queuedPrompt != "do not lose this" || got.steeringMsg != "" {
		t.Fatalf("verification submit handled=%v queue=%q steer=%q", handled, got.queuedPrompt, got.steeringMsg)
	}
	model, _ = onVerifyResult(got, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyFailed, Round: 1, Output: "command exited with code 1", Feedback: "failed",
	}})
	got = model.(*Model)
	if got.input.Value() != "do not lose this" || got.queuedPrompt != "" {
		t.Fatalf("exhaustion did not restore queued Enter: input=%q queue=%q", got.input.Value(), got.queuedPrompt)
	}
}

func TestVerificationErrorPreservesQueuedPromptAndCurrentDraft(t *testing.T) {
	m := scenarioModel(t)
	m.verifyEnabled = true
	m.verifying = true
	m.state = stateStreaming
	m.queuedPrompt = "submitted follow-up"
	m.blocks = append(m.blocks, block{kind: "user", body: m.queuedPrompt, queued: true})
	m.input.SetValue("unsent draft")
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"false"}, MaxRounds: 1}

	model, _ := onVerifyResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyFailed, Round: 1, Output: "failed", Feedback: "failed",
	}})
	got := model.(*Model)
	if got.input.Value() != "submitted follow-up\nunsent draft" || got.queuedPrompt != "" {
		t.Fatalf("restored input=%q queue=%q", got.input.Value(), got.queuedPrompt)
	}
}

func TestClearCancelsVerificationAndIgnoresStaleResult(t *testing.T) {
	m := scenarioModel(t)
	m.verifying = true
	m.verifyGeneration = 7
	m.state = stateStreaming
	m.blocks = append(m.blocks, block{kind: "system", body: "verifying"})
	cancelled := false
	m.toolCancel = func() { cancelled = true }

	m.handleSlash("/clear")
	if !cancelled || m.verifying || len(m.blocks) != 0 || m.state != stateIdle {
		t.Fatalf("clear cancelled=%v verifying=%v blocks=%d state=%v", cancelled, m.verifying, len(m.blocks), m.state)
	}
	model, cmd := onVerifyResult(m, verifyResultMsg{
		generation: 7,
		outcome:    runtime.VerifyOutcome{Status: runtime.VerifyFailed, Round: 1, Feedback: "stale"},
	})
	got := model.(*Model)
	if cmd != nil || len(got.blocks) != 0 || len(got.msgs) != 0 {
		t.Fatalf("stale verify result mutated cleared model: cmd=%v blocks=%d msgs=%d", cmd != nil, len(got.blocks), len(got.msgs))
	}
}

func TestVerifyPassResumesImmediateLoop(t *testing.T) {
	m := scenarioModel(t)
	m.verifyEnabled = true
	m.verifying = true
	m.state = stateStreaming
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"true"}, MaxRounds: 2}
	m.loop = &loopState{prompt: "repeat"}

	model, cmd := onVerifyResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyPassed, Round: 1,
	}})
	got := model.(*Model)
	if cmd == nil || got.loop == nil || got.loop.iter != 1 || got.state != stateStreaming {
		t.Fatalf("verified loop did not resume: cmd=%v loop=%+v state=%v", cmd != nil, got.loop, got.state)
	}
}

func TestNoToolSteerQueuesBeforeVerification(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.steeringMsg = "operator correction"
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"true"}, MaxRounds: 2}
	m.verifyEnabled = true
	m.rootCtx = context.Background()

	cmd := m.onTurnComplete()
	if cmd == nil || !m.verifying || m.queuedPrompt != "operator correction" || m.steeringMsg != "" {
		t.Fatalf("turn completion cmd=%v verifying=%v queue=%q steer=%q", cmd != nil, m.verifying, m.queuedPrompt, m.steeringMsg)
	}
	// Keep the compiler honest that the returned command remains a Bubble Tea
	// command without executing the verifier in this state-focused test.
	var _ tea.Cmd = cmd
}
