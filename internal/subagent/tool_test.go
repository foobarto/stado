package subagent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
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
	if !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRequestRejectsWorkspaceWriteUntilExposed(t *testing.T) {
	_, err := DecodeRequest(json.RawMessage(`{
		"prompt": "edit files",
		"role": "worker",
		"mode": "workspace_write",
		"ownership": "docs only",
		"write_scope": ["docs/**"]
	}`))
	if err == nil {
		t.Fatal("expected unsupported role error")
	}
	if !strings.Contains(err.Error(), "not supported yet") {
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
		{name: "windows absolute", scope: "C:/Users/foo", wantErr: "absolute"},
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

func TestToolRequiresSpawnerHost(t *testing.T) {
	res, err := (Tool{}).Run(context.Background(), json.RawMessage(`{"prompt":"inspect"}`), tools.NullHost{})
	if err == nil {
		t.Fatal("expected missing spawner error")
	}
	if res.Error == "" || !strings.Contains(res.Error, "does not support subagents") {
		t.Fatalf("result error = %q", res.Error)
	}
}

func TestToolClassIsNonMutating(t *testing.T) {
	if got := (Tool{}).Class(); got != tool.ClassNonMutating {
		t.Fatalf("Class = %v, want non-mutating", got)
	}
}
