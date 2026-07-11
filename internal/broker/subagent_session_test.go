package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateSession_SubagentProjectsParentEffectiveIntoManagedWorktree(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	svc := NewService(DefaultPolicy(), nil)
	parent, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: "/work",
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeSubagent, Profile: ProfileDefault, SessionID: parent.SessionID,
		Role: "worker", Mode: "workspace_write", WriteScope: []string{"src"},
	})
	if err != nil || !decision.Admit {
		t.Fatalf("create child: decision=%+v err=%v", decision, err)
	}
	childCWD := filepath.Join(stateHome, "stado", "worktrees", child.SessionID)
	if child.CWD != childCWD || child.Ceiling.CWD != childCWD {
		t.Fatalf("child cwd = %q/%q, want %q", child.CWD, child.Ceiling.CWD, childCWD)
	}
	wantWrite := childCWD
	if len(child.Ceiling.FSWrite) != 1 || child.Ceiling.FSWrite[0] != wantWrite {
		t.Fatalf("child writes = %v, want [%s]", child.Ceiling.FSWrite, wantWrite)
	}
	if len(parent.Ceiling.Mask) > 0 && len(child.Ceiling.Mask) == 0 {
		t.Fatal("child dropped parent credential masks")
	}
	if len(child.Ceiling.Sockets) != 0 {
		t.Fatalf("ordinary child inherited privileged sockets: %v", child.Ceiling.Sockets)
	}
}

func TestCreateSession_SubagentRejectsMissingParentAndCannotTargetAnotherWorktree(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	svc := NewService(DefaultPolicy(), nil)

	if _, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeSubagent, Profile: ProfileDefault, CWD: t.TempDir(),
	}); err == nil {
		t.Fatal("subagent without parent was admitted")
	}
	parent, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(stateHome, "stado", "worktrees", "foreign-session")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	child, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeSubagent, Profile: ProfileDefault, SessionID: parent.SessionID,
		CWD: foreign, Role: "explorer", Mode: "read_only",
	})
	if err != nil || !decision.Admit {
		t.Fatalf("broker-owned child: decision=%+v err=%v", decision, err)
	}
	if child.CWD == foreign || child.Ceiling.CWD == foreign {
		t.Fatalf("caller redirected child grant to foreign worktree: %+v", child)
	}
}

func TestDispatchSessionCreate_SubagentCarriesParentSession(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	svc := NewService(DefaultPolicy(), nil)
	parent, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(SessionCreateParams{
		Purpose: PurposeSubagent, Profile: ProfileDefault,
		ParentSessionID: parent.SessionID, Role: "explorer", Mode: "read_only",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Dispatch(t.Context(), MethodSessionCreate, raw)
	if err != nil {
		t.Fatalf("dispatch child: %v", err)
	}
	var handle SessionHandleResult
	if err := json.Unmarshal(result, &handle); err != nil {
		t.Fatal(err)
	}
	wantCWD := filepath.Join(stateHome, "stado", "worktrees", handle.SessionID)
	if handle.SessionID == "" || handle.CWD != wantCWD || handle.Ceiling.CWD != wantCWD {
		t.Fatalf("child result = %+v", handle)
	}
}
