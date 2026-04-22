package acp

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

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
}
