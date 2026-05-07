package acp

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// freshACPSession spins up a real git-native session under an
// isolated config + writes two prior messages to its conversation
// log. Returns the session id and worktree path so resume tests can
// drive handleSessionNew against authentic state instead of mocks.
func freshACPSession(t *testing.T, cfg *config.Config) (id, worktree string) {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoRoot)
	sess, err := runtime.OpenSession(cfg, repoRoot)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	prior := []agent.Message{
		agent.Text(agent.RoleUser, "previous question"),
		agent.Text(agent.RoleAssistant, "previous answer"),
	}
	if _, err := runtime.AppendMessagesFrom(sess.WorktreePath, prior, 0); err != nil {
		t.Fatalf("AppendMessagesFrom: %v", err)
	}
	return sess.ID, sess.WorktreePath
}

func TestHandleSessionNew_ResumeFromCLIDefault(t *testing.T) {
	cfg := isolatedACPConfig(t)
	id, _ := freshACPSession(t, cfg)

	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	srv.ResumeSessionID = id

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	gotID := res.(sessionNewResult).SessionID
	if gotID != id {
		t.Errorf("returned sessionId = %q, want %q (same as git id)", gotID, id)
	}
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess == nil {
		t.Fatalf("session %q not registered", id)
	}
	if len(sess.messages) != 2 {
		t.Errorf("messages loaded = %d, want 2 prior", len(sess.messages))
	}
	if sess.persistedViewLen != 2 {
		t.Errorf("persistedViewLen = %d, want 2 (prior count) so AppendMessagesFrom only persists new turns", sess.persistedViewLen)
	}
	if sess.gitSess == nil {
		t.Errorf("gitSess not attached after resume — handleSessionPrompt's ensureGitSession would re-open which loses the resume signal")
	}
}

func TestHandleSessionNew_ResumeParamWinsOverDefault(t *testing.T) {
	cfg := isolatedACPConfig(t)
	idA, _ := freshACPSession(t, cfg)
	idB, _ := freshACPSession(t, cfg)
	if idA == idB {
		t.Fatal("test setup: needed two distinct sessions")
	}

	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	srv.ResumeSessionID = idA

	raw := json.RawMessage(`{"resumeSession":"` + idB + `"}`)
	res, err := srv.handleSessionNew(raw)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	if got := res.(sessionNewResult).SessionID; got != idB {
		t.Errorf("session/new resumeSession should override --resume default; got %q want %q", got, idB)
	}
}

func TestHandleSessionNew_ResumeUnknownIDErrors(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	// A syntactically-valid but unknown UUID.
	raw := json.RawMessage(`{"resumeSession":"00000000-0000-0000-0000-000000000000"}`)
	_, err := srv.handleSessionNew(raw)
	if err == nil {
		t.Fatal("expected error for unknown resume id")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(rpcErr.Message, "no session worktree") {
		t.Errorf("error message should hint at missing worktree, got %q", rpcErr.Message)
	}
}

func TestHandleSessionNew_ResumeInvalidIDErrors(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	raw := json.RawMessage(`{"resumeSession":"not-a-uuid"}`)
	_, err := srv.handleSessionNew(raw)
	if err == nil {
		t.Fatal("expected error for malformed id")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(rpcErr.Message, "invalid session id") {
		t.Errorf("error message should call out invalid id format, got %q", rpcErr.Message)
	}
}

func TestHandleSessionNew_ResumeRejectsAlreadyActive(t *testing.T) {
	cfg := isolatedACPConfig(t)
	id, _ := freshACPSession(t, cfg)

	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)

	raw := json.RawMessage(`{"resumeSession":"` + id + `"}`)
	if _, err := srv.handleSessionNew(raw); err != nil {
		t.Fatalf("first resume failed: %v", err)
	}
	_, err := srv.handleSessionNew(raw)
	if err == nil {
		t.Fatal("expected error on second resume of the same id")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(rpcErr.Message, "already active") {
		t.Errorf("error message should mention already-active, got %q", rpcErr.Message)
	}
}

func TestHandleSessionNew_FreshSessionStillUsesACPNFormat(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	// No --resume default, no resumeSession param: classic acp-N flow.

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	gotID := res.(sessionNewResult).SessionID
	if !strings.HasPrefix(gotID, "acp-") {
		t.Errorf("fresh session id = %q, want acp-N prefix", gotID)
	}
}

// stadogit import only here to anchor the test against a real
// validation path; the rest of the suite drives through handleSessionNew.
var _ = stadogit.ValidateSessionID
