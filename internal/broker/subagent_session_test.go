package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestCreateSessionSubagentRegistrationIsAtomicWithParentMutation(t *testing.T) {
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

	entered := make(chan struct{})
	release := make(chan struct{})
	var nowCalls atomic.Int32
	svc.now = func() time.Time {
		if nowCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return time.Now()
	}
	childDone := make(chan error, 1)
	go func() {
		_, _, err := svc.CreateSession(CapabilityRequest{
			Purpose: PurposeSubagent, Profile: ProfileDefault, SessionID: parent.SessionID,
			Role: "explorer", Mode: "read_only",
		})
		childDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("child creation did not reach atomic registration section")
	}

	terminated := make(chan error, 1)
	go func() { terminated <- svc.TerminateSession(parent.SessionID, parent.controllerToken) }()
	select {
	case err := <-terminated:
		t.Fatalf("parent mutation interleaved with child registration: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-childDone; err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := <-terminated; err != nil {
		t.Fatalf("terminate parent: %v", err)
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
		ParentSessionID: parent.SessionID, ParentControllerToken: parent.controllerToken,
		Role: "explorer", Mode: "read_only",
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
	if handle.SessionID == "" || !strings.HasPrefix(handle.ControllerToken, "controller_") || handle.CWD != wantCWD || handle.Ceiling.CWD != wantCWD {
		t.Fatalf("child result = %+v", handle)
	}
}

func TestDispatchSessionCreateAuthenticatesParentWithoutLoggingBearer(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	writer := &MemoryWriter{}
	svc := NewService(DefaultPolicy(), writer)
	parent, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("parent=%+v decision=%+v err=%v", parent, decision, err)
	}
	dispatchCreate := func(params SessionCreateParams) ([]byte, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		return svc.Dispatch(t.Context(), MethodSessionCreate, raw)
	}
	if _, err := dispatchCreate(SessionCreateParams{
		Purpose: PurposeSubagent, Profile: ProfileDefault,
		ParentSessionID: parent.SessionID, ParentControllerToken: "controller_wrong",
		Role: "explorer", Mode: "read_only",
	}); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("subagent accepted wrong parent controller: %v", err)
	}
	childRaw, err := dispatchCreate(SessionCreateParams{
		Purpose: PurposeSubagent, Profile: ProfileDefault,
		ParentSessionID: parent.SessionID, ParentControllerToken: parent.controllerToken,
		Role: "explorer", Mode: "read_only",
	})
	if err != nil {
		t.Fatal(err)
	}
	var child SessionHandleResult
	if err := json.Unmarshal(childRaw, &child); err != nil {
		t.Fatal(err)
	}
	peerRaw, err := dispatchCreate(SessionCreateParams{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
		ParentSessionID: parent.SessionID, ParentControllerToken: parent.controllerToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	var peer SessionHandleResult
	if err := json.Unmarshal(peerRaw, &peer); err != nil {
		t.Fatal(err)
	}
	logged, err := json.Marshal(writer.Records())
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{parent.controllerToken, child.ControllerToken, peer.ControllerToken} {
		if strings.Contains(string(logged), token) {
			t.Fatal("session controller token entered a CapabilityRequest or Decision record")
		}
	}
}
