package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestVerifyInstalledPluginsABI_NilCfg(t *testing.T) {
	issues, err := VerifyInstalledPluginsABI(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil for nil cfg", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %v, want none", issues)
	}
}

func TestVerifyInstalledPluginsABI_NoInstalls(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	issues, err := VerifyInstalledPluginsABI(context.Background(), cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %v, want none for empty plugins dir", issues)
	}
}

func TestABIIssue_StringFormat(t *testing.T) {
	cases := []struct {
		name   string
		issue  ABIIssue
		expect []string
	}{
		{
			name: "missing_exports",
			issue: ABIIssue{
				Plugin:  "demo",
				Version: "0.1.0",
				Missing: []string{"stado_alloc", "stado_tool_run"},
			},
			expect: []string{"demo@0.1.0", "missing exports", "stado_alloc", "stado_tool_run"},
		},
		{
			name: "compile_error",
			issue: ABIIssue{
				Plugin:       "demo",
				Version:      "0.1.0",
				CompileError: "decoder: bad magic",
			},
			expect: []string{"demo@0.1.0", "wasm compile failed", "bad magic"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.issue.String()
			for _, want := range tc.expect {
				if !strings.Contains(got, want) {
					t.Errorf("String() = %q; missing %q", got, want)
				}
			}
		})
	}
}
