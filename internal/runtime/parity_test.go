package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// TestParityFS verifies that wasm fs.* tools produce identical output to
// their native counterparts. Skipped unless [runtime.use_wasm.fs] = true
// (set via STADO_PARITY_FS=1 env var for CI gating). EP-0038 D21.
func TestParityFS(t *testing.T) {
	if os.Getenv("STADO_PARITY_FS") != "1" {
		t.Skip("set STADO_PARITY_FS=1 to run fs parity tests")
	}
	runParityFamily(t, "fs", []parityCase{
		{
			nativeName: "read",
			wasmName:   "fs__read",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello parity\n"), 0o644)
			},
			cases: []toolCase{
				{name: "full_read", args: map[string]any{"path": "hello.txt"}},
			},
		},
		{
			nativeName: "glob",
			wasmName:   "fs__glob",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
				os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
			},
			cases: []toolCase{
				{name: "glob_txt", args: map[string]any{"pattern": "*.txt"}},
			},
		},
	})
}

// TestParityShell verifies shell.exec matches bash output. EP-0038 D21.
func TestParityShell(t *testing.T) {
	if os.Getenv("STADO_PARITY_SHELL") != "1" {
		t.Skip("set STADO_PARITY_SHELL=1 to run shell parity tests")
	}
	runParityFamily(t, "shell", []parityCase{
		{
			nativeName: "bash",
			wasmName:   "shell__exec",
			cases: []toolCase{
				{name: "echo", args: map[string]any{"command": "echo hello"}},
				{name: "exit_code", args: map[string]any{"command": "exit 1"}},
			},
		},
	})
}

// ── parity harness ────────────────────────────────────────────────────────

type parityCase struct {
	nativeName string
	wasmName   string
	setup      func(dir string)
	cases      []toolCase
}

type toolCase struct {
	name string
	args map[string]any
}

func runParityFamily(t *testing.T, family string, cases []parityCase) {
	t.Helper()
	dir := t.TempDir()

	// Native registry (no wasm flags set).
	nativeCfg := &config.Config{}
	nativeReg := runtime.BuildDefaultRegistry()
	runtime.ApplyToolFilter(nativeReg, nativeCfg)

	// Wasm registry (flag set for this family).
	wasmCfg := &config.Config{}
	wasmCfg.Runtime.UseWasm = map[string]bool{family: true}
	wasmReg := runtime.BuildDefaultRegistry()
	runtime.ApplyWasmMigration(wasmReg, wasmCfg)

	for _, pc := range cases {
		if pc.setup != nil {
			pc.setup(dir)
		}
		nativeTool, ok := nativeReg.Get(pc.nativeName)
		if !ok {
			t.Errorf("native tool %q not found", pc.nativeName)
			continue
		}
		wasmTool, ok := wasmReg.Get(pc.wasmName)
		if !ok {
			t.Errorf("wasm tool %q not found — was the wasm binary built?", pc.wasmName)
			continue
		}

		for _, tc := range pc.cases {
			tc := tc
			t.Run(pc.nativeName+"/"+tc.name, func(t *testing.T) {
				argsJSON, _ := json.Marshal(tc.args)
				h := &parityHost{workdir: dir}

				nativeResult, err := nativeTool.Run(context.Background(), argsJSON, h)
				if err != nil {
					t.Fatalf("native Run error: %v", err)
				}
				wasmResult, err := wasmTool.Run(context.Background(), argsJSON, h)
				if err != nil {
					t.Fatalf("wasm Run error: %v", err)
				}

				// Normalise: strip trailing whitespace, compare JSON-normalised.
				nativeNorm := normaliseToolResult(nativeResult.Content)
				wasmNorm := normaliseToolResult(wasmResult.Content)
				if nativeNorm != wasmNorm {
					t.Errorf("parity FAIL for %s/%s\nnative: %s\nwasm:   %s",
						pc.nativeName, tc.name, nativeResult.Content, wasmResult.Content)
				}
			})
		}
	}
}

func normaliseToolResult(s string) string {
	s = strings.TrimSpace(s)
	// If valid JSON, re-encode to normalise key order and whitespace.
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return s
}

// parityHost is a minimal tool.Host for parity tests.
type parityHost struct {
	workdir string
}

func (h *parityHost) Workdir() string { return h.workdir }
func (h *parityHost) Approve(_ context.Context, _ pkgtool.ApprovalRequest) (pkgtool.Decision, error) {
	return pkgtool.DecisionAllow, nil
}
func (h *parityHost) PriorRead(_ pkgtool.ReadKey) (pkgtool.PriorReadInfo, bool) {
	return pkgtool.PriorReadInfo{}, false
}
func (h *parityHost) RecordRead(_ pkgtool.ReadKey, _ pkgtool.PriorReadInfo) {}
