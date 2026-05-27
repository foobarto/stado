package broker

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestService_CreateSession_AdmitsAndMintsHandle(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	handle, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !decision.Admit {
		t.Fatalf("decision = %#v, want admit", decision)
	}
	if handle.SessionID == "" {
		t.Errorf("empty SessionID")
	}
	if len(handle.SessionID) != 32 {
		t.Errorf("SessionID len = %d, want 32 (hex of 16 random bytes)", len(handle.SessionID))
	}
	if handle.Purpose != PurposeMainChat {
		t.Errorf("Purpose = %q, want %q", handle.Purpose, PurposeMainChat)
	}
	if handle.TraceRef == "" {
		t.Errorf("TraceRef should be set for non-tool-run purposes")
	}
	if !strings.HasPrefix(handle.TraceRef, "refs/sessions/") {
		t.Errorf("TraceRef = %q, want refs/sessions/ prefix", handle.TraceRef)
	}
	if handle.CreatedAt.IsZero() {
		t.Errorf("CreatedAt zero")
	}
}

func TestService_CreateSession_RejectsInvalidPurpose(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	_, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: Purpose("bogus"),
		Profile: ProfileDefault,
	})
	if err == nil {
		t.Fatal("expected error from invalid purpose")
	}
	if !strings.Contains(err.Error(), "invalid purpose") {
		t.Errorf("err %q lacks 'invalid purpose'", err.Error())
	}
}

func TestService_CreateSession_RejectsInvalidProfile(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	_, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: Profile("bogus"),
	})
	if err == nil {
		t.Fatal("expected error from invalid profile")
	}
	if !strings.Contains(err.Error(), "invalid profile") {
		t.Errorf("err %q lacks 'invalid profile'", err.Error())
	}
}

func TestService_CreateSession_DeniedByPolicy(t *testing.T) {
	denyPolicy := &Policy{
		DefaultAdmit:  false,
		PurposeAdmits: map[Purpose]bool{PurposeMainChat: false},
	}
	svc := NewService(denyPolicy, nil)
	handle, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Admit {
		t.Errorf("decision.Admit = true, want false")
	}
	if handle.SessionID != "" {
		t.Errorf("denied request should not mint a session ID; got %q", handle.SessionID)
	}
}

func TestService_CreateSession_ToolRunHasEmptyTraceRef(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose:    PurposeToolRun,
		Profile:    ProfileDefault,
		PluginName: "fs.read",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if handle.TraceRef != "" {
		t.Errorf("PurposeToolRun should produce empty TraceRef; got %q", handle.TraceRef)
	}
}

func TestService_TerminateSession_Cycle(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.TerminateSession(handle.SessionID); err != nil {
		t.Fatalf("TerminateSession (first): %v", err)
	}
	if err := svc.TerminateSession(handle.SessionID); !errors.Is(err, ErrSessionTerminated) {
		t.Errorf("TerminateSession (second): err = %v, want ErrSessionTerminated", err)
	}
	if err := svc.TerminateSession("unknown"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("TerminateSession(unknown): err = %v, want ErrSessionNotFound", err)
	}
}

func TestService_LookupSession(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, terminated, err := svc.LookupSession(handle.SessionID)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if terminated {
		t.Errorf("freshly created session is terminated")
	}
	if got.SessionID != handle.SessionID {
		t.Errorf("LookupSession mismatch: got %q, want %q", got.SessionID, handle.SessionID)
	}
	if err := svc.TerminateSession(handle.SessionID); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}
	_, terminated, err = svc.LookupSession(handle.SessionID)
	if err != nil {
		t.Fatalf("LookupSession after terminate: %v", err)
	}
	if !terminated {
		t.Errorf("terminated session reports terminated=false")
	}
}

func TestService_LookupSession_Unknown(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	_, _, err := svc.LookupSession("not-a-real-id")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestService_DecisionLogged(t *testing.T) {
	mem := NewMemoryWriter()
	svc := NewService(DefaultPolicy(), mem)
	if _, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	records := mem.Records()
	if len(records) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(records))
	}
	if !records[0].Decision.Admit {
		t.Errorf("recorded decision.Admit = false, want true")
	}
	if records[0].Time.IsZero() {
		t.Errorf("recorded time zero")
	}
}

func TestService_SetPolicy_AppliesAtomically(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	d := svc.Evaluate(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault})
	if !d.Admit {
		t.Fatalf("initial Evaluate admit=false; expected admit")
	}
	svc.SetPolicy(&Policy{DefaultAdmit: false})
	d = svc.Evaluate(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault})
	if d.Admit {
		t.Errorf("after SetPolicy(deny), Evaluate still admits: %#v", d)
	}
}

func TestService_NowOverridable(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	fixed := time.Date(2026, 5, 27, 10, 30, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !handle.CreatedAt.Equal(fixed) {
		t.Errorf("CreatedAt = %v, want %v", handle.CreatedAt, fixed)
	}
}
