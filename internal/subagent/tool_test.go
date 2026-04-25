package subagent

import (
	"context"
	"encoding/json"
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

func TestDecodeRequestRejectsWriteMode(t *testing.T) {
	_, err := DecodeRequest(json.RawMessage(`{"prompt":"edit files","mode":"workspace"}`))
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
	if !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("error = %v", err)
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
