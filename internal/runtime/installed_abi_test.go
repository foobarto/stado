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
				Plugin:         "demo",
				Version:        "0.1.0",
				MissingExports: []string{"stado_alloc", "stado_tool_run"},
			},
			expect: []string{"demo@0.1.0", "missing exports", "stado_alloc", "stado_tool_run"},
		},
		{
			name: "removed_host_imports",
			issue: ABIIssue{
				Plugin:             "htb-toolkit",
				Version:            "0.4.2",
				RemovedHostImports: []string{"stado_fs_tool_read", "stado_fs_tool_write"},
			},
			expect: []string{"htb-toolkit@0.4.2", "imports removed", "stado_fs_tool_read", "rebuild required"},
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

// TestProvidedHostImports_HasCoreSet sanity-checks that the runtime's
// host-import set covers the expected primitives. Catches accidental
// removals (which would silently break the import-side ABI verifier
// from flagging plugins that depend on those imports).
func TestProvidedHostImports_HasCoreSet(t *testing.T) {
	provided, err := providedHostImports(context.Background())
	if err != nil {
		t.Fatalf("providedHostImports: %v", err)
	}
	wantPresent := []string{
		"stado_alloc", // ABI exports — wait, these are EXPORTS not imports
		"stado_log",
		"stado_fs_read",
		"stado_fs_write",
		"stado_fs_last_error",
		"stado_exec",
		"stado_progress",
	}
	for _, n := range wantPresent {
		if n == "stado_alloc" {
			// stado_alloc is a wasm-EXPORT (plugin → host), never a host-provided
			// function. Skip — this is just guarding against me misreading the API.
			if provided[n] {
				t.Errorf("stado_alloc unexpectedly in providedHostImports — host should never expose it")
			}
			continue
		}
		if !provided[n] {
			t.Errorf("missing host import %q in provided set; runtime broke a primitive?", n)
		}
	}
	wantAbsent := []string{
		"stado_fs_tool_read",    // removed in Step 7
		"stado_fs_tool_write",   // removed in Step 7
		"stado_fs_tool_edit",    // removed in Step 7
		"stado_search_ripgrep",  // removed in Step 5
		"stado_search_ast_grep", // removed in Step 5
	}
	for _, n := range wantAbsent {
		if provided[n] {
			t.Errorf("removed import %q still in provided set", n)
		}
	}
}
