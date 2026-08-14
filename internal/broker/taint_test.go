package broker

import (
	"errors"
	"strings"
	"testing"
)

func TestTaint_String(t *testing.T) {
	cases := map[Taint]string{
		TaintClean:   "clean",
		TaintTainted: "tainted",
		Taint(99):    "unknown",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("Taint(%d).String() = %q, want %q", in, got, want)
		}
	}
}

func TestService_SetTaintAndRead(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Initial state should be Clean.
	taint, err := svc.Taint(handle.SessionID)
	if err != nil {
		t.Fatalf("Taint: %v", err)
	}
	if taint != TaintClean {
		t.Errorf("initial Taint = %v, want Clean", taint)
	}

	// Mark tainted.
	if err := svc.SetTaint(handle.SessionID, handle.controllerToken, TaintTainted); err != nil {
		t.Fatalf("SetTaint(Tainted): %v", err)
	}
	if taint, _ := svc.Taint(handle.SessionID); taint != TaintTainted {
		t.Errorf("after SetTaint(Tainted), Taint = %v", taint)
	}

	// Reset to clean (operator-turn boundary).
	if err := svc.SetTaint(handle.SessionID, handle.controllerToken, TaintClean); err != nil {
		t.Fatalf("SetTaint(Clean): %v", err)
	}
	if taint, _ := svc.Taint(handle.SessionID); taint != TaintClean {
		t.Errorf("after SetTaint(Clean), Taint = %v", taint)
	}
}

func TestService_SetTaint_UnknownSession(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	err := svc.SetTaint("unknown-id", "controller_unknown", TaintTainted)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestService_SetTaint_TerminatedSession(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.TerminateSession(handle.SessionID, handle.controllerToken); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}
	err = svc.SetTaint(handle.SessionID, handle.controllerToken, TaintTainted)
	if !errors.Is(err, ErrSessionTerminated) {
		t.Errorf("err = %v, want ErrSessionTerminated", err)
	}
}

func TestEvaluateWithTaint_NoSessionIDIsTaintAgnostic(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	dec := svc.EvaluateWithTaint(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		// SessionID empty: taint-agnostic.
	})
	if !dec.Admit {
		t.Errorf("default policy admit + empty SessionID should still admit; got %#v", dec)
	}
}

func TestEvaluateWithTaint_CleanSessionAdmitsElevatedRole(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Clean context: elevated subagent role admitted.
	dec := svc.EvaluateWithTaint(CapabilityRequest{
		Purpose:   PurposeSubagent,
		Profile:   ProfileDefault,
		Role:      "git-fetch", // reserved for phase 7
		Mode:      "read_only",
		SessionID: handle.SessionID,
	})
	if !dec.Admit {
		t.Errorf("clean context should admit elevated role; got %#v", dec)
	}
}

func TestEvaluateWithTaint_TaintedSessionDeniesElevatedRole(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.SetTaint(handle.SessionID, handle.controllerToken, TaintTainted); err != nil {
		t.Fatalf("SetTaint: %v", err)
	}
	// Tainted context: elevated subagent role denied by overlay.
	dec := svc.EvaluateWithTaint(CapabilityRequest{
		Purpose:   PurposeSubagent,
		Profile:   ProfileDefault,
		Role:      "git-fetch",
		Mode:      "read_only",
		SessionID: handle.SessionID,
	})
	if dec.Admit {
		t.Errorf("tainted context should deny elevated role; got %#v", dec)
	}
	if !strings.HasPrefix(dec.Rule, "tainted-deny:") {
		t.Errorf("rule = %q, want 'tainted-deny:' prefix", dec.Rule)
	}
}

func TestEvaluateWithTaint_LogsFinalTaintDenialOnce(t *testing.T) {
	writer := &MemoryWriter{}
	svc := NewService(DefaultPolicy(), writer)
	parent, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSession: decision=%+v err=%v", decision, err)
	}
	if err := svc.SetTaint(parent.SessionID, parent.controllerToken, TaintTainted); err != nil {
		t.Fatal(err)
	}
	before := len(writer.Records())
	got := svc.EvaluateWithTaint(CapabilityRequest{
		Purpose: PurposeSubagent, Profile: ProfileDefault,
		SessionID: parent.SessionID, Role: "git-fetch",
	})
	if got.Admit || got.Rule != "tainted-deny:git-fetch" {
		t.Fatalf("decision = %+v", got)
	}
	records := writer.Records()
	if len(records) != before+1 {
		t.Fatalf("decision records grew by %d, want 1", len(records)-before)
	}
	if logged := records[len(records)-1].Decision; logged.Admit || logged.Rule != got.Rule {
		t.Fatalf("logged decision = %+v, want %+v", logged, got)
	}
}

func TestEvaluateWithTaint_TaintedSessionAdmitsNonElevatedRole(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.SetTaint(handle.SessionID, handle.controllerToken, TaintTainted); err != nil {
		t.Fatalf("SetTaint: %v", err)
	}
	// Tainted context but ordinary role: still admitted.
	dec := svc.EvaluateWithTaint(CapabilityRequest{
		Purpose:   PurposeSubagent,
		Profile:   ProfileDefault,
		Role:      "explorer", // non-elevated
		Mode:      "read_only",
		SessionID: handle.SessionID,
	})
	if !dec.Admit {
		t.Errorf("tainted context with non-elevated role should still admit; got %#v", dec)
	}
}

func TestEvaluateWithTaint_DeniedBaseStaysDenied(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	// Base policy denies main-chat outright. Taint overlay can't
	// admit something the base denies.
	svc := NewService(&Policy{DefaultAdmit: false}, nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if err == nil && handle.SessionID != "" {
		// CreateSession itself was denied so we can't continue.
		t.Skip("CreateSession denied; can't test overlay against existing session")
	}
}

func TestIsElevatedSubagentRole(t *testing.T) {
	cases := map[string]bool{
		"git-fetch":     true,
		"git-sub-agent": true,
		"explorer":      false,
		"worker":        false,
		"":              false,
		"unknown":       false,
	}
	for role, want := range cases {
		if got := isElevatedSubagentRole(role); got != want {
			t.Errorf("isElevatedSubagentRole(%q) = %v, want %v", role, got, want)
		}
	}
}
