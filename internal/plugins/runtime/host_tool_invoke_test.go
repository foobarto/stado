package runtime

import (
	"context"
	"testing"
)

func TestToolInvokeAccess_CanInvoke(t *testing.T) {
	cases := []struct {
		name   string
		globs  []string
		invoke string
		want   bool
	}{
		{"empty = match-all", nil, "anything.tool", true},
		{"exact match", []string{"fs.read"}, "fs.read", true},
		{"non-match", []string{"fs.read"}, "fs.write", false},
		{"glob fs.*", []string{"fs.*"}, "fs.read", true},
		{"glob fs.* doesn't match shell.exec", []string{"fs.*"}, "shell.exec", false},
		{"multiple globs", []string{"fs.read", "shell.*"}, "shell.spawn", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &ToolInvokeAccess{AllowedGlobs: tc.globs}
			if got := a.CanInvoke(tc.invoke); got != tc.want {
				t.Errorf("CanInvoke(%q) = %v, want %v", tc.invoke, got, tc.want)
			}
		})
	}
}

func TestToolInvokeAccess_NilSafety(t *testing.T) {
	var a *ToolInvokeAccess
	if a.CanInvoke("anything") {
		t.Error("nil ToolInvokeAccess.CanInvoke should be false")
	}
}

func TestInvokeDepth_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if d := CurrentInvokeDepth(ctx); d != 0 {
		t.Errorf("fresh ctx depth = %d, want 0", d)
	}
	ctx = WithInvokeDepth(ctx, 1)
	if d := CurrentInvokeDepth(ctx); d != 1 {
		t.Errorf("after WithInvokeDepth(1): %d, want 1", d)
	}
	ctx = WithInvokeDepth(ctx, 3)
	if d := CurrentInvokeDepth(ctx); d != 3 {
		t.Errorf("after WithInvokeDepth(3): %d, want 3", d)
	}
}
