package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

func installACPLifecycleFixture(t *testing.T, cfg *config.Config, id string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"quality","version":"1.0.0","wasm_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capabilities":[],"tools":[],"lifecycle":{}}`
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	mf, _, err := plugins.LoadFromDir(source)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, *mf)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.StateDir(), "plugins", record.StoreKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"plugin.manifest.json", "plugin.manifest.sig"} {
		data, readErr := os.ReadFile(filepath.Join(source, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(dir, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.WriteInstallRecord(dir, record, *mf); err != nil {
		t.Fatal(err)
	}
	return record.StoreKey
}

func TestACPSessionRejectsPersonaLifecycleApplicationBeforeBrokerWork(t *testing.T) {
	cfg := isolatedACPConfig(t)
	installedID := installACPLifecycleFixture(t, cfg, "quality-1.0.0")
	srv := NewServer(cfg, nil)
	srv.DefaultPersona = &personas.Persona{Name: "quality", Plugins: []string{installedID}}
	applications, err := runtime.ConfiguredLifecycleApplications(cfg, srv.DefaultPersona.Plugins)
	if err != nil || len(applications) != 1 {
		t.Fatalf("application classification = %#v, %v", applications, err)
	}
	brokerCalled := false
	srv.BrokerFactory = func(context.Context, string) (runtime.BrokerController, error) {
		brokerCalled = true
		return nil, nil
	}

	_, err = srv.handleSessionNew(nil)
	if err == nil || !strings.Contains(err.Error(), applications[0].CanonicalID) || !strings.Contains(err.Error(), "interactive TUI") {
		t.Fatalf("ACP lifecycle diagnostic = %v", err)
	}
	if brokerCalled || len(srv.sessions) != 0 {
		t.Fatalf("unsupported application reached broker/session work: broker=%v sessions=%d", brokerCalled, len(srv.sessions))
	}
}

type scriptedProvider struct {
	text string
}

func (p scriptedProvider) Name() string                     { return "scripted" }
func (p scriptedProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p scriptedProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	go func() {
		defer close(ch)
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: p.text}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

type blockingProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingProvider) Name() string                     { return "blocking" }
func (p *blockingProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *blockingProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	go func() {
		defer close(ch)
		p.once.Do(func() { close(p.started) })
		<-p.release
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: "done"}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

func isolatedACPConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestServerEmitSubagentUpdateIncludesWorkerFields(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(&config.Config{}, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), &out)

	srv.emitSubagentUpdate("session-1", runtime.SubagentEvent{
		Phase:           "finished",
		ParentSession:   "parent-1",
		ChildSession:    "child-1",
		Worktree:        "/tmp/child-1",
		Role:            "worker",
		Mode:            "workspace_write",
		Status:          "completed",
		TimeoutSeconds:  180,
		ForkTree:        "0123456789abcdef0123456789abcdef01234567",
		ChangedFiles:    []string{"docs/a.md"},
		ScopeViolations: []string{"blocked.txt: denied"},
	})

	var got Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	if got.Method != "session/update" {
		t.Fatalf("method = %q, want session/update", got.Method)
	}
	params, ok := got.Params.(map[string]any)
	if !ok {
		t.Fatalf("params type = %T", got.Params)
	}
	for _, key := range []string{"forkTree", "changedFiles", "scopeViolations"} {
		if _, ok := params[key]; !ok {
			t.Fatalf("params missing %s: %#v", key, params)
		}
	}
	if got := params["adoptionCommand"]; got != "stado session adopt parent-1 child-1 --fork-tree 0123456789abcdef0123456789abcdef01234567 --apply" {
		t.Fatalf("adoptionCommand = %v", got)
	}
	if params["kind"] != "subagent" || params["child"] != "child-1" {
		t.Fatalf("unexpected params: %#v", params)
	}
}

func TestServerRejectsOverlappingSessionPrompt(t *testing.T) {
	prov := &blockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv := NewServer(&config.Config{}, prov)
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := res.(sessionNewResult).SessionID
	first := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"first"}`)
	second := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"second"}`)

	done := make(chan error, 1)
	go func() {
		_, err := srv.handleSessionPrompt(context.Background(), first)
		done <- err
	}()

	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first prompt did not start")
	}

	_, err = srv.handleSessionPrompt(context.Background(), second)
	if err == nil || !strings.Contains(err.Error(), "active prompt") {
		t.Fatalf("second prompt error = %v, want active prompt rejection", err)
	}

	close(prov.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first prompt returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first prompt did not complete")
	}
}

func TestServerToolSessionsReuseGitStateAcrossPrompts(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.EnableTools = true
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := res.(sessionNewResult).SessionID

	for _, prompt := range []string{"one", "two"} {
		raw := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"` + prompt + `"}`)
		if _, err := srv.handleSessionPrompt(context.Background(), raw); err != nil {
			t.Fatalf("handleSessionPrompt(%q): %v", prompt, err)
		}
	}

	srv.mu.Lock()
	sess := srv.sessions[sessionID]
	srv.mu.Unlock()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.gitSess == nil {
		t.Fatal("git session was not created")
	}
	if len(sess.messages) != 4 {
		t.Fatalf("message count = %d, want 4", len(sess.messages))
	}
	if sess.messages[1].Role != agent.RoleAssistant || sess.messages[3].Role != agent.RoleAssistant {
		t.Fatalf("assistant turns were not persisted: %+v", sess.messages)
	}
	if sess.persistedViewLen != 4 {
		t.Fatalf("persistedViewLen = %d, want 4", sess.persistedViewLen)
	}
	loaded, err := runtime.LoadConversation(sess.gitSess.WorktreePath)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(loaded) != 4 {
		t.Fatalf("persisted conversation = %d messages, want 4", len(loaded))
	}
}

// TestResolveMaxTurnsPrecedence covers the per-session > config >
// built-in-default precedence chain — the contract that lets ACP
// callers pin engagement turn budgets without recompiling stado.
func TestResolveMaxTurnsPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		cfgMax      int
		sessionMax  int
		enableTools bool
		want        int
	}{
		{"session_pin_wins", 7, 42, true, 42},
		{"cfg_when_no_pin", 7, 0, true, 7},
		{"tools_default_when_unset", 0, 0, true, 50},
		{"chat_default_when_unset", 0, 0, false, 1},
		{"session_pin_overrides_chat_default", 0, 99, false, 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.ACP.MaxTurns = tc.cfgMax
			srv := NewServer(cfg, nil)
			srv.EnableTools = tc.enableTools
			sess := &acpSession{maxTurns: tc.sessionMax}
			if got := srv.resolveMaxTurns(sess); got != tc.want {
				t.Fatalf("resolveMaxTurns = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSessionNewAcceptsMaxTurnsParam covers the wire-format end of
// session/new — `{"maxTurns": N}` lands on acpSession.maxTurns.
func TestSessionNewAcceptsMaxTurnsParam(t *testing.T) {
	srv := NewServer(&config.Config{}, nil)
	res, err := srv.handleSessionNew(json.RawMessage(`{"maxTurns": 25}`))
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID
	srv.mu.Lock()
	got := srv.sessions[id].maxTurns
	srv.mu.Unlock()
	if got != 25 {
		t.Fatalf("session.maxTurns = %d, want 25", got)
	}
}
