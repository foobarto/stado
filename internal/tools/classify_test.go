package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/tool"
)

// TestClassOf_BuiltIns: post-Step-7 of EP-no-internal-tools, only
// `tasks` remains as a non-plugin native tool with a static class
// entry. fs.* and readctx.read register their classes via
// newBundledWasmTool calls in bundled_plugin_tools.go.
func TestClassOf_BuiltIns(t *testing.T) {
	r := NewRegistry()
	r.Register(tasktool.Tool{})

	cases := map[string]tool.Class{
		"tasks": tool.ClassStateMutating,
	}
	for name, want := range cases {
		if got := r.ClassOf(name); got != want {
			t.Errorf("ClassOf(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestClassOf_UnknownTool(t *testing.T) {
	r := NewRegistry()
	if got := r.ClassOf("nonexistent"); got != tool.ClassExec {
		t.Errorf("ClassOf unknown = %v, want Exec (safe default)", got)
	}
}

// Custom tool implementing Classifier takes precedence over the name map.
type customTool struct{}

func (customTool) Name() string           { return "custom" }
func (customTool) Description() string    { return "" }
func (customTool) Schema() map[string]any { return nil }
func (customTool) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	return tool.Result{}, nil
}
func (customTool) Class() tool.Class { return tool.ClassExec }

func TestClassOf_ClassifierInterface(t *testing.T) {
	r := NewRegistry()
	r.Register(customTool{})
	if got := r.ClassOf("custom"); got != tool.ClassExec {
		t.Errorf("per-instance Classifier ignored: got %v, want Exec", got)
	}
}

func TestClass_String(t *testing.T) {
	if tool.ClassMutating.String() != "mutating" {
		t.Errorf("string mutation class wrong: %q", tool.ClassMutating.String())
	}
	if tool.ClassStateMutating.String() != "state-mutating" {
		t.Errorf("string state mutation class wrong: %q", tool.ClassStateMutating.String())
	}
	if tool.ClassExec.String() != "exec" {
		t.Errorf("string exec class wrong: %q", tool.ClassExec.String())
	}
	if tool.ClassNonMutating.String() != "non-mutating" {
		t.Errorf("string nonmut class wrong: %q", tool.ClassNonMutating.String())
	}
}
