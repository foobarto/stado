package runtime

import (
	"context"
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
