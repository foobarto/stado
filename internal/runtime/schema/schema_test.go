package schema_test

import (
	"reflect"
	"testing"

	"github.com/foobarto/stado/internal/runtime/schema"
)

func TestObject_OmitsEmptyRequiredAndProps(t *testing.T) {
	got := schema.Object(nil, nil)
	want := map[string]any{"type": "object"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestObject_IncludesRequiredAndProps(t *testing.T) {
	got := schema.Object([]string{"path"}, schema.Props{
		"path": schema.String(),
	})
	want := map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": schema.Props{
			"path": map[string]any{"type": "string"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestObject_DefensiveCopyOfRequired(t *testing.T) {
	req := []string{"path"}
	got := schema.Object(req, schema.Props{"path": schema.String()})
	req[0] = "mutated"
	gotReq, _ := got["required"].([]string)
	if gotReq[0] != "path" {
		t.Fatalf("required[0] = %q, want %q (caller mutation should not affect schema)", gotReq[0], "path")
	}
}

func TestObject_DefensiveCopyOfProps(t *testing.T) {
	p := schema.Props{"path": schema.String()}
	got := schema.Object([]string{"path"}, p)
	p["other"] = schema.String()
	gotProps, _ := got["properties"].(schema.Props)
	if _, exists := gotProps["other"]; exists {
		t.Fatalf("caller mutation of input props leaked into schema")
	}
}

func TestString_WithDescription(t *testing.T) {
	got := schema.String("a path")
	want := map[string]any{"type": "string", "description": "a path"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestString_NoDescription(t *testing.T) {
	got := schema.String()
	want := map[string]any{"type": "string"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestString_EmptyDescriptionTreatedAsNone(t *testing.T) {
	got := schema.String("")
	if _, has := got["description"]; has {
		t.Fatalf("empty description should not be stored: %v", got)
	}
}

func TestInteger_NumberBoolean(t *testing.T) {
	if got := schema.Integer("count"); got["type"] != "integer" || got["description"] != "count" {
		t.Fatalf("Integer = %v", got)
	}
	if got := schema.Number(); got["type"] != "number" {
		t.Fatalf("Number = %v", got)
	}
	if got := schema.Boolean("flag"); got["type"] != "boolean" || got["description"] != "flag" {
		t.Fatalf("Boolean = %v", got)
	}
}

func TestArray_WithItemsAndDescription(t *testing.T) {
	got := schema.Array(schema.String(), "list of names")
	want := map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": "list of names",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestStringEnum(t *testing.T) {
	got := schema.StringEnum([]string{"a", "b", "c"}, "pick one")
	enum, _ := got["enum"].([]any)
	if len(enum) != 3 || enum[0] != "a" || enum[2] != "c" {
		t.Fatalf("enum = %v", enum)
	}
	if got["description"] != "pick one" {
		t.Fatalf("description missing: %v", got)
	}
}

func TestEmpty(t *testing.T) {
	got := schema.Empty()
	want := map[string]any{"type": "object"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestComposed mirrors a real bundled-tool schema (fs__edit, hashline
// anchor form) to confirm the composed shape — including a nested
// array-of-object for edits — matches what the wasm host expects.
func TestComposed_FsEditShape(t *testing.T) {
	got := schema.Object([]string{"path", "edits"}, schema.Props{
		"path": schema.String(),
		"edits": schema.Array(schema.Object([]string{"op", "lines"}, schema.Props{
			"op":    schema.StringEnum([]string{"replace", "append", "prepend"}),
			"pos":   schema.String(),
			"end":   schema.String(),
			"lines": schema.Array(schema.String()),
		})),
	})
	if got["type"] != "object" {
		t.Fatalf("type = %v", got["type"])
	}
	req, _ := got["required"].([]string)
	if len(req) != 2 || req[0] != "path" || req[1] != "edits" {
		t.Fatalf("required = %v", req)
	}
	props, _ := got["properties"].(schema.Props)
	if len(props) != 2 {
		t.Fatalf("properties count = %d, want 2", len(props))
	}
	edits, _ := props["edits"].(map[string]any)
	if edits["type"] != "array" {
		t.Fatalf("edits type = %v, want array", edits["type"])
	}
	item, _ := edits["items"].(map[string]any)
	if item["type"] != "object" {
		t.Fatalf("edits.items type = %v, want object", item["type"])
	}
}
