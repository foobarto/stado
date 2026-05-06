package runtime

import (
	"context"
	"strings"
	"testing"
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
