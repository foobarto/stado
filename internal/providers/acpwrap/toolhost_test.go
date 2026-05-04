package acpwrap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/pkg/tool"
)

// fakeTool captures Run() inputs for assertion and returns canned
// outputs.
type fakeTool struct {
	name        string
	gotArgs     json.RawMessage
	gotHost     tool.Host
	returnRes   tool.Result
	returnErr   error
}

func (f *fakeTool) Name() string                      { return f.name }
func (f *fakeTool) Description() string               { return "" }
func (f *fakeTool) Schema() map[string]any            { return nil }
func (f *fakeTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	f.gotArgs = args
	f.gotHost = h
	return f.returnRes, f.returnErr
}

// stubHost is a no-op tool.Host suitable for passing into tests where
// the host's behaviour isn't under test (we're verifying the
// translation layer, not the tool itself).
type stubHost struct{ workdir string }

func (h *stubHost) Approve(ctx context.Context, req tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h *stubHost) Workdir() string                                  { return h.workdir }
func (h *stubHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) { return tool.PriorReadInfo{}, false }
func (h *stubHost) RecordRead(tool.ReadKey, tool.PriorReadInfo)       {}

func TestRequestHandler_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: &fakeTool{name: "read"},
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	_, err := h(context.Background(), "does/not/exist", json.RawMessage(`{}`))
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *acp.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != acp.CodeMethodNotFound {
		t.Errorf("code = %d, want %d", rpcErr.Code, acp.CodeMethodNotFound)
	}
}

func TestReadTextFile_FullFile_TranslatesPath(t *testing.T) {
	read := &fakeTool{name: "read", returnRes: tool.Result{Content: "hello\nworld\n"}}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: read,
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{workdir: "/tmp"},
	})
	params := json.RawMessage(`{"sessionId":"s1","path":"/abs/file.txt"}`)
	got, err := h(context.Background(), "fs/read_text_file", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the read tool got translated args (path only, no
	// start/end).
	var stadoArgs struct {
		Path  string `json:"path"`
		Start *int   `json:"start"`
		End   *int   `json:"end"`
	}
	if err := json.Unmarshal(read.gotArgs, &stadoArgs); err != nil {
		t.Fatalf("read.gotArgs not valid JSON: %v", err)
	}
	if stadoArgs.Path != "/abs/file.txt" {
		t.Errorf("path = %q, want %q", stadoArgs.Path, "/abs/file.txt")
	}
	if stadoArgs.Start != nil || stadoArgs.End != nil {
		t.Errorf("expected start/end nil for full-file read, got start=%v end=%v", stadoArgs.Start, stadoArgs.End)
	}

	// Verify the response shape.
	res, ok := got.(acpReadResult)
	if !ok {
		t.Fatalf("expected acpReadResult, got %T: %+v", got, got)
	}
	if res.Content != "hello\nworld\n" {
		t.Errorf("content = %q, want %q", res.Content, "hello\nworld\n")
	}
}

func TestReadTextFile_LineAndLimit_TranslatesToStartEnd(t *testing.T) {
	read := &fakeTool{name: "read", returnRes: tool.Result{Content: "x"}}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: read,
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	// line=10, limit=5 → start=10, end=14
	params := json.RawMessage(`{"sessionId":"s1","path":"/x","line":10,"limit":5}`)
	if _, err := h(context.Background(), "fs/read_text_file", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var stadoArgs struct {
		Start *int `json:"start"`
		End   *int `json:"end"`
	}
	if err := json.Unmarshal(read.gotArgs, &stadoArgs); err != nil {
		t.Fatalf("parse stadoArgs: %v", err)
	}
	if stadoArgs.Start == nil || *stadoArgs.Start != 10 {
		t.Errorf("start = %v, want 10", stadoArgs.Start)
	}
	if stadoArgs.End == nil || *stadoArgs.End != 14 {
		t.Errorf("end = %v, want 14", stadoArgs.End)
	}
}

func TestReadTextFile_LineOnly_NoLimit_OmitsEnd(t *testing.T) {
	read := &fakeTool{name: "read", returnRes: tool.Result{Content: "x"}}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: read,
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	params := json.RawMessage(`{"sessionId":"s1","path":"/x","line":5}`)
	if _, err := h(context.Background(), "fs/read_text_file", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var stadoArgs struct {
		Start *int `json:"start"`
		End   *int `json:"end"`
	}
	_ = json.Unmarshal(read.gotArgs, &stadoArgs)
	if stadoArgs.Start == nil || *stadoArgs.Start != 5 {
		t.Errorf("start = %v, want 5", stadoArgs.Start)
	}
	if stadoArgs.End != nil {
		t.Errorf("expected end nil (read to EOF), got %v", *stadoArgs.End)
	}
}

func TestReadTextFile_MissingPath_ReturnsInvalidParams(t *testing.T) {
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: &fakeTool{name: "read"},
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	_, err := h(context.Background(), "fs/read_text_file", json.RawMessage(`{"sessionId":"s1"}`))
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected RPCError, got %v", err)
	}
	if rpcErr.Code != acp.CodeInvalidParams {
		t.Errorf("code = %d, want CodeInvalidParams", rpcErr.Code)
	}
}

func TestReadTextFile_ToolReturnsError_BecomesInternalError(t *testing.T) {
	read := &fakeTool{name: "read", returnErr: errors.New("permission denied")}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: read,
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	_, err := h(context.Background(), "fs/read_text_file", json.RawMessage(`{"sessionId":"s1","path":"/x"}`))
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected RPCError, got %v", err)
	}
	if rpcErr.Code != acp.CodeInternalError {
		t.Errorf("code = %d, want CodeInternalError", rpcErr.Code)
	}
	if !strings.Contains(rpcErr.Message, "permission denied") {
		t.Errorf("message = %q, expected to contain underlying error", rpcErr.Message)
	}
}

func TestReadTextFile_ToolResultErrorString_Surfaces(t *testing.T) {
	// Tools sometimes return (Result{Error: "..."}, nil) — the
	// error string should still surface in the ACP response.
	read := &fakeTool{name: "read", returnRes: tool.Result{Error: "file not found"}}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: read,
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	_, err := h(context.Background(), "fs/read_text_file", json.RawMessage(`{"sessionId":"s1","path":"/x"}`))
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected RPCError, got %v", err)
	}
	if !strings.Contains(rpcErr.Message, "file not found") {
		t.Errorf("message = %q, expected to contain tool error", rpcErr.Message)
	}
}

func TestWriteTextFile_TranslatesArgs(t *testing.T) {
	write := &fakeTool{name: "write"}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: &fakeTool{name: "read"},
		WriteTool: write,
		Host:     &stubHost{},
	})
	params := json.RawMessage(`{"sessionId":"s1","path":"/abs/x","content":"new\n"}`)
	got, err := h(context.Background(), "fs/write_text_file", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result (ACP spec returns null on success), got %v", got)
	}

	var stadoArgs struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(write.gotArgs, &stadoArgs); err != nil {
		t.Fatalf("parse stadoArgs: %v", err)
	}
	if stadoArgs.Path != "/abs/x" {
		t.Errorf("path = %q", stadoArgs.Path)
	}
	if stadoArgs.Content != "new\n" {
		t.Errorf("content = %q", stadoArgs.Content)
	}
}

func TestWriteTextFile_MissingPath_InvalidParams(t *testing.T) {
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: &fakeTool{name: "read"},
		WriteTool: &fakeTool{name: "write"},
		Host:     &stubHost{},
	})
	_, err := h(context.Background(), "fs/write_text_file", json.RawMessage(`{"sessionId":"s1","content":"x"}`))
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected RPCError, got %v", err)
	}
	if rpcErr.Code != acp.CodeInvalidParams {
		t.Errorf("code = %d, want CodeInvalidParams", rpcErr.Code)
	}
}

func TestHandlers_NilToolOrHost_InternalError(t *testing.T) {
	// Defence-in-depth: with nil ReadTool/Host, the handler must
	// not panic — it returns InternalError.
	h := BuildRequestHandler(ToolHostConfig{}) // all nil
	_, err := h(context.Background(), "fs/read_text_file", json.RawMessage(`{"sessionId":"s1","path":"/x"}`))
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected RPCError, got %v", err)
	}
	if rpcErr.Code != acp.CodeInternalError {
		t.Errorf("code = %d, want CodeInternalError", rpcErr.Code)
	}
}

func TestRequestPermission_PrefersAllowAlways(t *testing.T) {
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool:  &fakeTool{},
		WriteTool: &fakeTool{},
		Host:      &stubHost{},
	})
	// Spec-canonical option set. Auto-approver must select an
	// allow_always option (most permissive) over allow_once.
	params := json.RawMessage(`{
		"sessionId":"s1",
		"toolCall":{"toolCallId":"c1"},
		"options":[
			{"optionId":"once","name":"Allow once","kind":"allow_once"},
			{"optionId":"always","name":"Allow always","kind":"allow_always"},
			{"optionId":"reject","name":"Reject","kind":"reject_once"}
		]
	}`)
	got, err := h(context.Background(), "session/request_permission", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, ok := got.(acpPermissionResult)
	if !ok {
		t.Fatalf("expected acpPermissionResult, got %T", got)
	}
	if res.Outcome.Outcome != "selected" {
		t.Errorf("outcome.outcome = %q, want %q", res.Outcome.Outcome, "selected")
	}
	if res.Outcome.OptionID != "always" {
		t.Errorf("optionId = %q, want %q (must prefer allow_always over allow_once)", res.Outcome.OptionID, "always")
	}
}

func TestRequestPermission_FallsBackToAllowOnce(t *testing.T) {
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool:  &fakeTool{},
		WriteTool: &fakeTool{},
		Host:      &stubHost{},
	})
	params := json.RawMessage(`{
		"sessionId":"s1",
		"toolCall":{"toolCallId":"c1"},
		"options":[
			{"optionId":"once","name":"Allow once","kind":"allow_once"},
			{"optionId":"reject","name":"Reject","kind":"reject_once"}
		]
	}`)
	got, _ := h(context.Background(), "session/request_permission", params)
	res := got.(acpPermissionResult)
	if res.Outcome.OptionID != "once" {
		t.Errorf("optionId = %q, want %q", res.Outcome.OptionID, "once")
	}
}

func TestRequestPermission_AcceptsNonStandardAllowKind(t *testing.T) {
	// gemini-cli observed emitting non-canonical kinds like
	// "allow_always_server". Ensure the prefix-fallback catches them
	// rather than returning cancelled.
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool:  &fakeTool{},
		WriteTool: &fakeTool{},
		Host:      &stubHost{},
	})
	params := json.RawMessage(`{
		"sessionId":"s1",
		"toolCall":{"toolCallId":"c1"},
		"options":[
			{"optionId":"server","name":"Allow all","kind":"allow_always_server"},
			{"optionId":"reject","name":"Reject","kind":"reject_once"}
		]
	}`)
	got, _ := h(context.Background(), "session/request_permission", params)
	res := got.(acpPermissionResult)
	if res.Outcome.OptionID != "server" {
		t.Errorf("optionId = %q, want %q (prefix-match should catch allow_always_server)", res.Outcome.OptionID, "server")
	}
}

func TestRequestPermission_NoAllowOption_ReturnsCancelled(t *testing.T) {
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool:  &fakeTool{},
		WriteTool: &fakeTool{},
		Host:      &stubHost{},
	})
	params := json.RawMessage(`{
		"sessionId":"s1",
		"toolCall":{"toolCallId":"c1"},
		"options":[
			{"optionId":"reject","name":"Reject","kind":"reject_once"}
		]
	}`)
	got, _ := h(context.Background(), "session/request_permission", params)
	res := got.(acpPermissionResult)
	if res.Outcome.Outcome != "cancelled" {
		t.Errorf("outcome = %q, want %q (should refuse to invent an allow when none offered)", res.Outcome.Outcome, "cancelled")
	}
}

func TestReadTextFile_HostPassedThrough(t *testing.T) {
	// The translation layer must pass the configured Host through
	// to the underlying tool unchanged — this is where the
	// permission/sandbox stack hooks in (D7).
	read := &fakeTool{name: "read", returnRes: tool.Result{Content: "x"}}
	host := &stubHost{workdir: "/specific"}
	h := BuildRequestHandler(ToolHostConfig{
		ReadTool: read,
		WriteTool: &fakeTool{name: "write"},
		Host:     host,
	})
	if _, err := h(context.Background(), "fs/read_text_file", json.RawMessage(`{"sessionId":"s1","path":"/x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if read.gotHost != host {
		t.Errorf("read tool got host = %v, want %v (Host must pass through unchanged)", read.gotHost, host)
	}
}
