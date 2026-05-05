package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/tool"
)

// TestMCPServer_ToolsExposedWithSchemas: every stado tool registers
// with the MCP server and each schema round-trips as valid JSON.
// Without this, a typo'd schema on some bundled tool would silently
// produce `{"type":"object"}` and external MCP clients would lose
// argument hints — catching the regression here is cheaper than
// debugging it inside Claude Desktop.
func TestMCPServer_ToolsExposedWithSchemas(t *testing.T) {
	reg := runtime.BuildDefaultRegistry(nil)
	srv := server.NewMCPServer("stado-test", "0.0.0-test")
	runner := sandbox.Detect()
	host := stadoMCPHost{workdir: t.TempDir(), runner: runner}
	executor := &tools.Executor{Registry: reg, Runner: runner, Metrics: telemetry.Metrics{}, Agent: "test"}
	for _, tl := range reg.All() {
		registerStadoTool(srv, tl, host, executor)
	}

	// Verify each schema we'd marshal is actually valid JSON, and the
	// required top-level "type" field survives the round-trip.
	for _, tl := range reg.All() {
		raw := rawSchema(tl.Schema())
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Errorf("tool %s: schema marshalled to invalid JSON: %v", tl.Name(), err)
			continue
		}
		if _, ok := decoded["type"]; !ok {
			t.Errorf("tool %s: schema missing 'type' key: %s", tl.Name(), raw)
		}
	}
}

func TestMCPServer_ExposesTasksTool(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := &config.Config{}
	reg := runtime.BuildDefaultRegistry(nil)
	reg.Register(tasktool.Tool{Path: tasks.StorePath(cfg.StateDir())})
	runtime.ApplyToolFilter(reg, cfg)

	if _, ok := reg.Get("tasks"); !ok {
		t.Fatal("tasks tool missing from MCP registry")
	}
}

// TestRawSchema_NilAndErrorFallbacks: defensive coverage for the
// schema marshalling fallback path. A broken schema must not
// take down the MCP server — it falls back to a permissive "any
// object" so the tool stays callable (client just sees no hints).
func TestRawSchema_NilAndErrorFallbacks(t *testing.T) {
	// Nil map → permissive object.
	nilRaw := rawSchema(nil)
	if !strings.Contains(string(nilRaw), `"type":"object"`) {
		t.Errorf("nil schema fallback wrong: %s", nilRaw)
	}
	// Unmarshallable map (json.Marshal can't encode a channel) →
	// same permissive fallback, not a panic.
	bad := map[string]any{"ch": make(chan int)}
	badRaw := rawSchema(bad)
	if !strings.Contains(string(badRaw), `"type":"object"`) {
		t.Errorf("error-path schema fallback wrong: %s", badRaw)
	}
}

// TestStadoMCPHost_AutoApproves: the MCP host auto-allows every
// approval request. The client is the authz boundary in mcp-server
// mode; stado trusts the caller.
func TestStadoMCPHost_AutoApproves(t *testing.T) {
	runner := sandbox.Detect()
	h := stadoMCPHost{workdir: "/tmp", runner: runner}
	decision, err := h.Approve(context.Background(),
		tool.ApprovalRequest{Tool: "any", Command: "any"})
	if err != nil {
		t.Errorf("unexpected approve error: %v", err)
	}
	if decision != tool.DecisionAllow {
		t.Errorf("expected DecisionAllow, got %v", decision)
	}
	if h.Workdir() != "/tmp" {
		t.Errorf("Workdir lost: %q", h.Workdir())
	}
	if h.Runner() == nil {
		t.Error("Runner() must expose the configured sandbox runner so bash gets confined")
	}
	// PriorRead never hits since we have no log.
	if _, ok := h.PriorRead(tool.ReadKey{Path: "x"}); ok {
		t.Error("PriorRead should always be miss on the MCP host")
	}
}

// TestStadoMCPHost_RunnerInterfaceAssertable: the bash tool detects
// the sandbox runner via an interface type-assert (`h.(interface{
// Runner() sandbox.Runner })`). If the host stops exposing Runner()
// for any reason, bash silently runs unsandboxed — this test catches
// the regression by asserting the interface satisfies as bash would.
func TestStadoMCPHost_RunnerInterfaceAssertable(t *testing.T) {
	h := stadoMCPHost{workdir: "/tmp", runner: sandbox.Detect()}
	var asTool tool.Host = h
	rh, ok := asTool.(interface{ Runner() sandbox.Runner })
	if !ok {
		t.Fatal("stadoMCPHost no longer satisfies Runner() interface — bash will silently run unsandboxed")
	}
	if rh.Runner() == nil {
		t.Error("Runner() returned nil — bash will silently run unsandboxed")
	}
}
