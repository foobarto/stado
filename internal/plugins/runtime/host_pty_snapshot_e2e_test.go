package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/plugins"
)

// TestShellSnapshotE2E drives the full chain: instantiate the bundled
// shell.wasm, spawn a `cat` PTY, write a known string, then call
// stado_tool_snapshot and verify the host JSON makes it back through
// the wasm boundary intact. Catches regressions in:
//   - host import wire format (id round-trip, JSON shape)
//   - bundled plugin's stadoToolSnapshot dispatch + buffer sizing
//   - vt10x integration (text actually shows up in the snapshot)
//
// One module instance drives every tool call — wazero refuses
// duplicate instantiations of the same {name, version} and the PTY
// registry lives on the host anyway, so re-instantiation gains
// nothing.
func TestShellSnapshotE2E(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	mf := plugins.Manifest{
		Name:         "shell",
		Version:      "1.0.0",
		Capabilities: []string{"exec:pty"},
		Tools: []plugins.ToolDef{
			{Name: "spawn", Class: "Exec"},
			{Name: "attach", Class: "Exec"},
			{Name: "write", Class: "Exec"},
			{Name: "snapshot", Class: "NonMutating"},
		},
	}
	host := NewHost(mf, t.TempDir(), nil)
	host.ToolHost = toolImportHost{workdir: t.TempDir()}
	if err := InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	mod, err := rt.Instantiate(ctx, bundledplugins.MustWasm("shell"), mf)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	tools := map[string]*PluginTool{}
	for _, td := range mf.Tools {
		pt, err := NewPluginTool(mod, td)
		if err != nil {
			t.Fatalf("NewPluginTool(%s): %v", td.Name, err)
		}
		tools[td.Name] = pt
	}
	invoke := func(name, argsJSON string) string {
		t.Helper()
		res, err := tools[name].Run(ctx, json.RawMessage(argsJSON), host.ToolHost)
		if err != nil {
			t.Fatalf("Run(%s): %v", name, err)
		}
		if res.Error != "" {
			t.Fatalf("%s tool error: %q", name, res.Error)
		}
		return res.Content
	}

	// 1. Spawn a long-lived `cat`.
	out := invoke("spawn", `{"argv":["/bin/cat"],"cols":80,"rows":24}`)
	var spawnRes struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &spawnRes); err != nil {
		t.Fatalf("spawn unmarshal: %v (raw %q)", err, out)
	}
	id := spawnRes.ID.String()
	if id == "" || id == "0" {
		t.Fatalf("spawn returned bad id: %q", out)
	}
	defer invoke("snapshot", `{"id":`+id+`}`) // best-effort cleanup peek; CloseAll on rt.Close handles destroy.

	// 2. Attach + write the marker. Cat echoes stdin → stdout.
	invoke("attach", `{"id":`+id+`}`)
	invoke("write", `{"id":`+id+`,"data":"snapshot-marker\n"}`)

	// 3. Poll snapshot until the marker appears (max 2s).
	deadline := time.Now().Add(2 * time.Second)
	var lastSnap string
	for time.Now().Before(deadline) {
		lastSnap = invoke("snapshot", `{"id":`+id+`}`)
		if strings.Contains(lastSnap, "snapshot-marker") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(lastSnap, "snapshot-marker") {
		t.Fatalf("snapshot never contained marker. last result:\n%s", lastSnap)
	}

	// 4. Sanity-check the JSON shape — text and dims must be present,
	//    SVG must be absent without with_svg.
	var snap struct {
		Text string `json:"text"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
		SVG  string `json:"svg"`
	}
	if err := json.Unmarshal([]byte(lastSnap), &snap); err != nil {
		t.Fatalf("snapshot unmarshal: %v (raw %q)", err, lastSnap)
	}
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Errorf("snapshot dims = %dx%d, want 80x24", snap.Cols, snap.Rows)
	}
	if snap.SVG != "" {
		t.Errorf("snapshot SVG present without with_svg=true (got %d bytes)", len(snap.SVG))
	}

	// 5. with_svg=true: SVG payload should be present and well-formed.
	withSVG := invoke("snapshot", `{"id":`+id+`,"with_svg":true}`)
	if err := json.Unmarshal([]byte(withSVG), &snap); err != nil {
		t.Fatalf("with_svg snapshot unmarshal: %v", err)
	}
	if !strings.HasPrefix(snap.SVG, "<svg") || !strings.HasSuffix(snap.SVG, "</svg>") {
		head := snap.SVG
		if len(head) > 80 {
			head = head[:80]
		}
		t.Errorf("with_svg result not well-formed:\n%s", head)
	}
	if !strings.Contains(snap.SVG, "snapshot-marker") {
		t.Errorf("SVG missing marker text:\n%s", snap.SVG)
	}
}
