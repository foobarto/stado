package headless

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
)

func loadBundledDefaultPersona(t *testing.T) *personas.Persona {
	t.Helper()
	p, err := personas.Resolver{}.Load("default")
	if err != nil {
		t.Fatalf("load bundled default persona: %v", err)
	}
	return p
}

func TestSessionNew_DefaultPersonaPropagates(t *testing.T) {
	srv := NewServer(&config.Config{}, nil)
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)
	srv.DefaultPersona = loadBundledDefaultPersona(t)

	res, err := srv.sessionNew(nil)
	if err != nil {
		t.Fatalf("sessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess == nil || sess.persona == nil {
		t.Fatal("operator default persona should propagate to new session")
	}
	if sess.persona.Name != srv.DefaultPersona.Name {
		t.Errorf("session persona = %q, want %q", sess.persona.Name, srv.DefaultPersona.Name)
	}
}

func TestSessionNew_PerCallPersonaResolves(t *testing.T) {
	srv := NewServer(&config.Config{}, nil)
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)

	raw := json.RawMessage(`{"persona":"default"}`)
	res, err := srv.sessionNew(raw)
	if err != nil {
		t.Fatalf("sessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess == nil || sess.persona == nil {
		t.Fatal("per-call persona should populate session")
	}
}

func TestSessionNew_UnknownPersonaErrors(t *testing.T) {
	srv := NewServer(&config.Config{}, nil)
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)

	raw := json.RawMessage(`{"persona":"this-persona-does-not-exist-xyz"}`)
	_, err := srv.sessionNew(raw)
	if err == nil {
		t.Fatal("expected error for unknown persona")
	}
	rpcErr, ok := err.(*acp.RPCError)
	if !ok || rpcErr.Code != acp.CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(rpcErr.Message, "persona") {
		t.Errorf("error message should mention persona, got %q", rpcErr.Message)
	}
}
