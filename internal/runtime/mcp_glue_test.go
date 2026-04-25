package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tools"
)

func TestAttachMCP_NoServers_NoOp(t *testing.T) {
	reg := tools.NewRegistry()
	// Empty server map should be handled by the caller, but attachMCP with
	// an empty map should also be safe.
	if err := attachMCP(reg, map[string]config.MCPServer{}); err != nil {
		t.Errorf("attachMCP with empty servers: %v", err)
	}
}

func TestAttachMCP_BadCommand_Error(t *testing.T) {
	reg := tools.NewRegistry()
	err := attachMCP(reg, map[string]config.MCPServer{
		"bogus": {
			Command:      "/nonexistent/binary/should-fail",
			Capabilities: []string{"net:deny"},
		},
	})
	if err == nil {
		t.Error("expected error for non-existent MCP server command")
	}
}

func TestAttachMCP_StdioRequiresCapabilities(t *testing.T) {
	reg := tools.NewRegistry()
	err := attachMCP(reg, map[string]config.MCPServer{
		"bogus": {Command: "/nonexistent/binary/should-fail"},
	})
	if err == nil {
		t.Fatal("expected error for stdio MCP server without capabilities")
	}
	if !strings.Contains(err.Error(), "capabilities are required") {
		t.Fatalf("error %q missing capability requirement", err)
	}
}

func TestAttachMCPStatusSnapshotRecordsErrors(t *testing.T) {
	reg := tools.NewRegistry()
	_ = attachMCP(reg, map[string]config.MCPServer{
		"bogus": {Command: "/nonexistent/binary/should-fail"},
	})

	snap := MCPStatusSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1: %#v", len(snap), snap)
	}
	if snap[0].Name != "bogus" || snap[0].Connected || !strings.Contains(snap[0].Error, "capabilities are required") {
		t.Fatalf("snapshot = %#v", snap[0])
	}
}

func TestJoinErrors_FormatsMultiple(t *testing.T) {
	errs := []error{errors.New("first"), errors.New("second")}
	out := joinErrors(errs)
	if !strings.Contains(out.Error(), "first") || !strings.Contains(out.Error(), "second") {
		t.Errorf("joined error missing parts: %v", out)
	}
}

func TestJoinErrors_SingleUnwraps(t *testing.T) {
	only := errors.New("solo")
	if got := joinErrors([]error{only}); got != only {
		t.Errorf("single-element join should return the error unchanged")
	}
}
