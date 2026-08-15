package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

func TestToolMetadataForBundledToolComesFromManifest(t *testing.T) {
	registered, ok := BuildDefaultRegistry(nil).Get("fs__read")
	if !ok {
		t.Fatal("fs__read missing")
	}
	metadata := ToolMetadataFor(registered)
	if metadata.Canonical != "fs.read" || metadata.Plugin != "stado-builtin-tool-fs" || metadata.PackageNamespace != "stado.dev/bundled/fs" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if len(metadata.Categories) != 1 || metadata.Categories[0] != "filesystem" {
		t.Fatalf("categories = %v", metadata.Categories)
	}
}

func TestToolMetadataForNativeDebtUsesOnlyWireProjection(t *testing.T) {
	registered := namedStubTool("example__inspect")
	metadata := ToolMetadataFor(registered)
	if metadata.Canonical != "example.inspect" || metadata.Plugin != "example" {
		t.Fatalf("metadata = %+v", metadata)
	}
}

type namedStubTool string

func (t namedStubTool) Name() string         { return string(t) }
func (namedStubTool) Description() string    { return "" }
func (namedStubTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (namedStubTool) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	return tool.Result{}, nil
}
