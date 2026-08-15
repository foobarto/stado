package subagent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeRequestDefaultsAndCaps(t *testing.T) {
	req, err := DecodeRequest(json.RawMessage(`{"prompt":"inspect session code","max_turns":99}`))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Role != DefaultRole {
		t.Fatalf("role = %q, want %q", req.Role, DefaultRole)
	}
	if req.Mode != DefaultMode {
		t.Fatalf("mode = %q, want %q", req.Mode, DefaultMode)
	}
	if req.MaxTurns != MaxTurns {
		t.Fatalf("max_turns = %d, want cap %d", req.MaxTurns, MaxTurns)
	}
	if req.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Fatalf("timeout_seconds = %d, want default %d", req.TimeoutSeconds, DefaultTimeoutSeconds)
	}
}

func TestDecodeRequestHistoricalModelAndRetention(t *testing.T) {
	raw := json.RawMessage(`{"prompt":"continue analysis","source":{"session_id":"old","at":"turns/3"},"model":"configured-model","execution":"retained","token_budget":12000}`)
	req, err := DecodeRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Source == nil || req.Source.SessionID != "old" || req.Source.At != "turns/3" || req.Model != "configured-model" || req.Execution != "retained" || req.TokenBudget != 12000 {
		t.Fatalf("req=%+v", req)
	}
}

func TestDecodeRequestProviderReasoningProfile(t *testing.T) {
	raw := json.RawMessage(`{
		"prompt":"review the exact turn",
		"provider":" anthropic ",
		"model":" claude-sonnet-4-6 ",
		"thinking":" on ",
		"thinking_budget_tokens":12000,
		"reasoning_effort":" high ",
		"token_budget":20000
	}`)
	req, err := DecodeRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Provider != "anthropic" || req.Model != "claude-sonnet-4-6" || req.Thinking != "on" ||
		req.ThinkingBudgetTokens != 12000 || req.ReasoningEffort != "high" {
		t.Fatalf("provider profile = %#v", req)
	}
}

func TestDecodeRequestRejectsInvalidProviderReasoningProfile(t *testing.T) {
	tests := []string{
		`{"prompt":"x","provider":"anthropic"}`,
		`{"prompt":"x","thinking":"sometimes"}`,
		`{"prompt":"x","thinking_budget_tokens":2000001}`,
		`{"prompt":"x","thinking_budget_tokens":101,"token_budget":100}`,
		`{"prompt":"x","reasoning_effort":"unbounded"}`,
		`{"prompt":"x","provider":"bad\u0000name","model":"m"}`,
	}
	for _, raw := range tests {
		if _, err := DecodeRequest(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid provider profile %s", raw)
		}
	}
}

func TestDecodeRequestRejectsMutableOrAmbiguousSource(t *testing.T) {
	for _, raw := range []string{`{"prompt":"x","source":{"session_id":"s","at":"tip"}}`, `{"prompt":"x","source":{"at":"turns/1"}}`, `{"prompt":"x","execution":"maybe"}`} {
		if _, err := DecodeRequest(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestDecodeRequestCapsTimeout(t *testing.T) {
	req, err := DecodeRequest(json.RawMessage(`{"prompt":"inspect","timeout_seconds":99999}`))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.TimeoutSeconds != MaxTimeoutSeconds {
		t.Fatalf("timeout_seconds = %d, want cap %d", req.TimeoutSeconds, MaxTimeoutSeconds)
	}
}

func TestDecodeRequestNormalizesWriteScope(t *testing.T) {
	req, err := DecodeRequest(json.RawMessage(`{
		"prompt": "inspect",
		"write_scope": [" internal/foo/** ", "docs/foo.md", "docs/foo.md", "*.md"]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	want := []string{"internal/foo/**", "docs/foo.md", "*.md"}
	if !reflect.DeepEqual(req.WriteScope, want) {
		t.Fatalf("write_scope = %#v, want %#v", req.WriteScope, want)
	}
}

func TestDecodeRequestRejectsWriteMode(t *testing.T) {
	_, err := DecodeRequest(json.RawMessage(`{"prompt":"edit files","mode":"workspace"}`))
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRequestAcceptsWorkspaceWriteWorker(t *testing.T) {
	req, err := DecodeRequest(json.RawMessage(`{
		"prompt": "edit files",
		"role": "worker",
		"mode": "workspace_write",
		"ownership": "docs only",
		"write_scope": [" docs/** ", "docs/**"]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Role != WorkerRole || req.Mode != WorkspaceWriteMode {
		t.Fatalf("role/mode = %s/%s", req.Role, req.Mode)
	}
	if want := []string{"docs/**"}; !reflect.DeepEqual(req.WriteScope, want) {
		t.Fatalf("write_scope = %#v, want %#v", req.WriteScope, want)
	}
}

func TestDecodeRequestRejectsWorkspaceWriteWithoutScope(t *testing.T) {
	_, err := DecodeRequest(json.RawMessage(`{
		"prompt": "edit files",
		"role": "worker",
		"mode": "workspace_write",
		"ownership": "docs only"
	}`))
	if err == nil {
		t.Fatal("expected missing write_scope error")
	}
	if !strings.Contains(err.Error(), "write_scope is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRequestRejectsWorkspaceWriteWithoutOwnership(t *testing.T) {
	_, err := DecodeRequest(json.RawMessage(`{
		"prompt": "edit files",
		"role": "worker",
		"mode": "workspace_write",
		"write_scope": ["docs/**"]
	}`))
	if err == nil {
		t.Fatal("expected missing ownership error")
	}
	if !strings.Contains(err.Error(), "ownership is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeWriteScopeAcceptsRepoRelativeGlobs(t *testing.T) {
	got, err := NormalizeWriteScope([]string{
		" internal/foo/** ",
		"docs/foo.md",
		"docs/foo.md",
		"*.md",
		"./cmd/stado",
	})
	if err != nil {
		t.Fatalf("NormalizeWriteScope: %v", err)
	}
	want := []string{"internal/foo/**", "docs/foo.md", "*.md", "cmd/stado"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope = %#v, want %#v", got, want)
	}
}

func TestNormalizeWriteScopeRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name    string
		scope   string
		wantErr string
	}{
		{name: "empty", scope: "", wantErr: "empty"},
		{name: "absolute", scope: "/etc/passwd", wantErr: "absolute"},
		{name: "drive absolute", scope: "C:/Users/foo", wantErr: "absolute"},
		{name: "parent traversal", scope: "../x", wantErr: ".."},
		{name: "interior traversal", scope: "foo/../bar", wantErr: ".."},
		{name: "git metadata", scope: ".git/config", wantErr: ".git"},
		{name: "stado metadata", scope: "foo/.stado/state", wantErr: ".stado"},
		{name: "backslash", scope: `foo\bar`, wantErr: "backslashes"},
		{name: "root", scope: ".", wantErr: "repository root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeWriteScope([]string{tt.scope})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
