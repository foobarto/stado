package acp

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/personas"
)

// loadBundledDefault is the easiest way to get a real *personas.Persona
// in a test — it always exists in the embedded library, so tests don't
// have to spin up a temp config dir to populate one.
func loadBundledDefaultPersona(t *testing.T) *personas.Persona {
	t.Helper()
	p, err := personas.Resolver{}.Load("default")
	if err != nil {
		t.Fatalf("load bundled default persona: %v", err)
	}
	return p
}

func TestHandleSessionNew_DefaultPersonaPropagatesToSession(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	srv.DefaultPersona = loadBundledDefaultPersona(t)

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess == nil {
		t.Fatalf("session not registered")
	}
	if sess.persona == nil {
		t.Fatal("session persona nil; expected the operator-pinned default")
	}
	if sess.persona.Name != srv.DefaultPersona.Name {
		t.Errorf("session persona = %q, want %q", sess.persona.Name, srv.DefaultPersona.Name)
	}
}

func TestHandleSessionNew_PerCallPersonaWinsOverDefault(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	// Operator default is bundled "default".
	srv.DefaultPersona = loadBundledDefaultPersona(t)

	// Per-call the same name (it's the only one we know exists in
	// the bundled library without a fixture). The point of the test
	// is that the per-call path runs through resolveSessionPersona
	// and yields a populated session, NOT that the resolved object
	// differs — that's covered by the "no per-call → default" test.
	raw := json.RawMessage(`{"persona":"default"}`)
	res, err := srv.handleSessionNew(raw)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess == nil || sess.persona == nil {
		t.Fatal("expected per-call persona to populate session")
	}
}

func TestHandleSessionNew_UnknownPersonaErrors(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	raw := json.RawMessage(`{"persona":"this-persona-does-not-exist-xyz"}`)
	_, err := srv.handleSessionNew(raw)
	if err == nil {
		t.Fatal("expected error for unknown persona")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(rpcErr.Message, "persona") {
		t.Errorf("error message should mention persona, got %q", rpcErr.Message)
	}
}

// TestHandleSessionNew_ResumeKeepsPersonaResolution verifies the
// per-call persona param applies even on the resume path — both
// branches go through the same resolveSessionPersona seam.
func TestHandleSessionNew_ResumeKeepsPersonaResolution(t *testing.T) {
	cfg := isolatedACPConfig(t)
	id, _ := freshACPSession(t, cfg)

	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	raw := json.RawMessage(`{"resumeSession":"` + id + `","persona":"default"}`)
	res, err := srv.handleSessionNew(raw)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	if got := res.(sessionNewResult).SessionID; got != id {
		t.Fatalf("returned id = %q, want %q", got, id)
	}
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess.persona == nil {
		t.Error("resumed session lost the per-call persona; the assignment after buildResumedSession should populate it")
	}
}
