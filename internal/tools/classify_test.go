package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/pkg/tool"
)

func TestClassOf_BuiltIns(t *testing.T) {
	r := NewRegistry()
	r.Register(bash.BashTool{Timeout: time.Second})
	r.Register(fs.ReadTool{})
	r.Register(fs.WriteTool{})
	r.Register(fs.EditTool{})
	r.Register(fs.GlobTool{})
	r.Register(fs.GrepTool{})
	r.Register(webfetch.WebFetchTool{})

	cases := map[string]tool.Class{
		"bash":     tool.ClassExec,
		"read":     tool.ClassNonMutating,
		"write":    tool.ClassMutating,
		"edit":     tool.ClassMutating,
		"glob":     tool.ClassNonMutating,
		"grep":     tool.ClassNonMutating,
		"webfetch": tool.ClassNonMutating,
	}
	for name, want := range cases {
		if got := r.ClassOf(name); got != want {
			t.Errorf("ClassOf(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestClassOf_UnknownTool(t *testing.T) {
	r := NewRegistry()
	if got := r.ClassOf("nonexistent"); got != tool.ClassNonMutating {
		t.Errorf("ClassOf unknown = %v, want NonMutating (safe default)", got)
	}
}

// Custom tool implementing Classifier takes precedence over the name map.
type customTool struct{}

func (customTool) Name() string                                                { return "custom" }
func (customTool) Description() string                                         { return "" }
func (customTool) Schema() map[string]any                                      { return nil }
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
	if tool.ClassExec.String() != "exec" {
		t.Errorf("string exec class wrong: %q", tool.ClassExec.String())
	}
	if tool.ClassNonMutating.String() != "non-mutating" {
		t.Errorf("string nonmut class wrong: %q", tool.ClassNonMutating.String())
	}
}
