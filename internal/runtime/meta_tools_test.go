package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// TestMetaSearch_RejectsMalformedJSON: malformed args used to silently
// default to empty query (audit-additions item #16). They should now
// return an error.
func TestMetaSearch_RejectsMalformedJSON(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaSearch{reg: reg}
	_, err := tool.Run(context.Background(), []byte("{not valid json"), nil)
	if err == nil {
		t.Error("metaSearch.Run should error on malformed JSON args; got nil")
	}
}

func TestMetaCategories_RejectsMalformedJSON(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaCategories{reg: reg}
	_, err := tool.Run(context.Background(), []byte("{not valid"), nil)
	if err == nil {
		t.Error("metaCategories.Run should error on malformed JSON args; got nil")
	}
}

// Also pin: the *valid* path still works (regression check after the
// error-handling change).
func TestMetaSearch_ValidJSON(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaSearch{reg: reg}
	_, err := tool.Run(context.Background(), []byte(`{"query":"fs"}`), nil)
	if err != nil {
		t.Errorf("valid args should succeed; got %v", err)
	}
}

func TestMetaCategories_ValidJSON(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaCategories{reg: reg}
	_, err := tool.Run(context.Background(), []byte(`{"query":"file"}`), nil)
	if err != nil {
		t.Errorf("valid args should succeed; got %v", err)
	}
}

// TestMetaDescribe_SingleName: `name` (string) selects one tool.
func TestMetaDescribe_SingleName(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaDescribe{reg: reg}
	res, err := tool.Run(context.Background(), []byte(`{"name":"read"}`), nil)
	if err != nil {
		t.Fatalf("single-name describe: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("res.Error = %q", res.Error)
	}
	if !strings.Contains(res.Content, `"name":"read"`) {
		t.Errorf("expected `read` entry in content; got: %s", res.Content)
	}
}

// TestMetaDescribe_NamesArray: `names` (array) batches.
func TestMetaDescribe_NamesArray(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaDescribe{reg: reg}
	res, err := tool.Run(context.Background(), []byte(`{"names":["read","write"]}`), nil)
	if err != nil {
		t.Fatalf("batched describe: %v", err)
	}
	if !strings.Contains(res.Content, `"name":"read"`) {
		t.Errorf("expected `read`; got: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"name":"write"`) {
		t.Errorf("expected `write`; got: %s", res.Content)
	}
}

// TestMetaDescribe_BothNameAndNames: `name` + `names` merge with dedupe.
func TestMetaDescribe_BothNameAndNames(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaDescribe{reg: reg}
	res, err := tool.Run(context.Background(), []byte(`{"name":"read","names":["read","write"]}`), nil)
	if err != nil {
		t.Fatalf("merged describe: %v", err)
	}
	// `read` should appear exactly once in the entries list.
	if got := strings.Count(res.Content, `"name":"read"`); got != 1 {
		t.Errorf("expected exactly one `read` entry; got %d in: %s", got, res.Content)
	}
	if !strings.Contains(res.Content, `"name":"write"`) {
		t.Errorf("expected `write`; got: %s", res.Content)
	}
}

// TestMetaDescribe_EmptyArgs: no name and no names is an error.
func TestMetaDescribe_EmptyArgs(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaDescribe{reg: reg}
	res, _ := tool.Run(context.Background(), []byte(`{}`), nil)
	if res.Error == "" {
		t.Error("expected Result.Error to be set; got empty")
	}
}

// fakeActivatorHost is a minimal tool.Host that implements
// ToolActivator + ToolDeactivator for meta-tool tests.
type fakeActivatorHost struct {
	activated   map[string]bool
	deactivated map[string]bool
}

func newFakeActivatorHost() *fakeActivatorHost {
	return &fakeActivatorHost{
		activated:   map[string]bool{},
		deactivated: map[string]bool{},
	}
}

func (h *fakeActivatorHost) Approve(context.Context, pkgtool.ApprovalRequest) (pkgtool.Decision, error) {
	return pkgtool.DecisionAllow, nil
}
func (h *fakeActivatorHost) Workdir() string         { return "/tmp" }
func (h *fakeActivatorHost) Runner() sandbox.Runner  { return sandbox.NoneRunner{} }
func (h *fakeActivatorHost) RequestApproval(context.Context, string, string) (bool, error) {
	return true, nil
}
func (h *fakeActivatorHost) PriorRead(pkgtool.ReadKey) (pkgtool.PriorReadInfo, bool) {
	return pkgtool.PriorReadInfo{}, false
}
func (h *fakeActivatorHost) RecordRead(pkgtool.ReadKey, pkgtool.PriorReadInfo) {}
func (h *fakeActivatorHost) ActivateTool(name string)   { h.activated[name] = true }
func (h *fakeActivatorHost) DeactivateTool(name string) { h.deactivated[name] = true }

// TestMetaActivate_AddsToActivationSet: tools__activate calls the
// host's ActivateTool for each known tool name.
func TestMetaActivate_AddsToActivationSet(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaActivate{reg: reg}
	host := newFakeActivatorHost()

	res, err := tool.Run(context.Background(), []byte(`{"name":"read"}`), host)
	if err != nil {
		t.Fatalf("activate single: %v", err)
	}
	if res.Error != "" {
		t.Errorf("res.Error = %q", res.Error)
	}
	if !host.activated["read"] {
		t.Errorf("expected `read` in activated set; got %v", host.activated)
	}

	host = newFakeActivatorHost()
	res, err = tool.Run(context.Background(), []byte(`{"names":["read","grep"]}`), host)
	if err != nil {
		t.Fatalf("activate batch: %v", err)
	}
	if !host.activated["read"] || !host.activated["grep"] {
		t.Errorf("expected both `read` and `grep` activated; got %v", host.activated)
	}
}

// TestMetaActivate_NoHostSupport: returns an error result when the host
// doesn't implement ToolActivator.
func TestMetaActivate_NoHostSupport(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaActivate{reg: reg}
	res, _ := tool.Run(context.Background(), []byte(`{"name":"read"}`), nil)
	if res.Error == "" {
		t.Error("expected Result.Error when host is nil")
	}
}

// TestMetaDeactivate_RemovesFromSet: tools__deactivate calls
// DeactivateTool for each name.
func TestMetaDeactivate_RemovesFromSet(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaDeactivate{reg: reg}
	host := newFakeActivatorHost()
	host.activated["read"] = true

	_, err := tool.Run(context.Background(), []byte(`{"name":"read"}`), host)
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if !host.deactivated["read"] {
		t.Errorf("expected `read` in deactivated set")
	}
}

// TestMetaPluginLoad_ActivatesAllToolsForPlugin: plugin__load activates
// every tool whose metadata says it belongs to the named plugin.
func TestMetaPluginLoad_ActivatesAllToolsForPlugin(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaPluginLoad{reg: reg}
	host := newFakeActivatorHost()

	// `agent` plugin owns at least agent__spawn / agent__list (per
	// internal/runtime/bundled_plugin_tools.go's registrations).
	res, err := tool.Run(context.Background(), []byte(`{"plugin":"agent"}`), host)
	if err != nil {
		t.Fatalf("plugin__load: %v", err)
	}
	if res.Error != "" {
		t.Errorf("res.Error = %q (content: %s)", res.Error, res.Content)
	}
	if !strings.Contains(res.Content, "agent__spawn") {
		t.Errorf("expected `agent__spawn` in result; got: %s", res.Content)
	}
	if len(host.activated) == 0 {
		t.Error("expected ActivateTool to be called at least once")
	}
}

// TestMetaPluginLoad_UnknownPluginReturnsError: plugin__load against an
// unknown plugin name → Result.Error.
func TestMetaPluginLoad_UnknownPluginReturnsError(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaPluginLoad{reg: reg}
	host := newFakeActivatorHost()
	res, _ := tool.Run(context.Background(), []byte(`{"plugin":"nope-no-such"}`), host)
	if res.Error == "" {
		t.Error("expected error for unknown plugin")
	}
}

// TestMetaDescribe_UnknownToolReturnsErrorEntry: a not-found name
// becomes an error entry, not a hard fail.
func TestMetaDescribe_UnknownToolReturnsErrorEntry(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	tool := &metaDescribe{reg: reg}
	res, err := tool.Run(context.Background(), []byte(`{"name":"nope_no_such"}`), nil)
	if err != nil {
		t.Fatalf("unknown name should not hard-fail: %v", err)
	}
	if !strings.Contains(res.Content, `"error":"not found"`) {
		t.Errorf("expected `not found` error entry; got: %s", res.Content)
	}
}
