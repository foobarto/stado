package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
)

// TestShellReadModesE2E drives the full chain: instantiate the bundled
// shell.wasm, spawn a `cat` PTY, write a known string, then exercise the
// EP-0043 folded read: mode:"screen" returns the rendered vt100 screen
// (the former shell.screenshot) and mode:"auto" returns the raw stream
// because `cat` is not a full-screen program. Catches regressions in:
//   - host import wire format (id round-trip, JSON shape)
//   - the read mode dispatch (screen via the snapshot import; auto's
//     {kind:"stream"} fall-through)
//   - vt10x integration (text shows up in the screen render)
//
// No explicit attach (EP-0043 D6 — read/write work by id). One module
// instance drives every tool call.
func TestShellReadModesE2E(t *testing.T) {
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
			{Name: "spawn", Class: "Exec", Capabilities: plugins.CapabilitySubset("exec:pty")},
			{Name: "write", Class: "Exec", Capabilities: plugins.CapabilitySubset("exec:pty")},
			{Name: "read", Class: "NonMutating", Capabilities: plugins.CapabilitySubset("exec:pty")},
			{Name: "destroy", Class: "Exec", Capabilities: plugins.CapabilitySubset("exec:pty")},
		},
	}
	host := NewHost(mf, t.TempDir(), nil)
	host.ToolHost = toolImportHost{workdir: t.TempDir()}
	if err := InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	mod, err := rt.Instantiate(ctx, bundled.MustWasm("shell"), mf)
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
	// Cleanup: destroy the spawned PTY so the cat child doesn't
	// outlive the test as an orphan (parented to PID 1 and alive
	// until reboot or manual kill). Earlier this only did a snapshot
	// "peek" for cleanup, which leaked /bin/cat processes across
	// repeated test runs and contributed to a multi-process OOM
	// during a `go test ./...` storm on a dev machine. Destroy is
	// idempotent — already-closed sessions return without erroring.
	defer invoke("destroy", `{"id":`+id+`}`)

	// 2. Write the marker — no attach (EP-0043 D6). Cat echoes stdin.
	invoke("write", `{"id":`+id+`,"data":"snapshot-marker\n"}`)

	// 3. Poll read mode:"screen" until the marker appears in the render.
	deadline := time.Now().Add(2 * time.Second)
	var lastSnap string
	for time.Now().Before(deadline) {
		lastSnap = invoke("read", `{"id":`+id+`,"mode":"screen"}`)
		if strings.Contains(lastSnap, "snapshot-marker") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(lastSnap, "snapshot-marker") {
		t.Fatalf("read mode:screen never contained marker. last result:\n%s", lastSnap)
	}

	// 4. Sanity-check the JSON shape — kind:"screen", text + dims present,
	//    SVG absent without with_svg.
	var snap struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
		SVG  string `json:"svg"`
	}
	if err := json.Unmarshal([]byte(lastSnap), &snap); err != nil {
		t.Fatalf("read mode:screen unmarshal: %v (raw %q)", err, lastSnap)
	}
	if snap.Kind != "screen" {
		t.Errorf("read mode:screen kind = %q, want screen", snap.Kind)
	}
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Errorf("screen dims = %dx%d, want 80x24", snap.Cols, snap.Rows)
	}
	if snap.SVG != "" {
		t.Errorf("screen SVG present without with_svg=true (got %d bytes)", len(snap.SVG))
	}

	// 5. with_svg=true: SVG payload present and well-formed.
	withSVG := invoke("read", `{"id":`+id+`,"mode":"screen","with_svg":true}`)
	if err := json.Unmarshal([]byte(withSVG), &snap); err != nil {
		t.Fatalf("with_svg unmarshal: %v", err)
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

	// 6. mode:"auto" on `cat` (not a full-screen program → not on the
	//    alternate screen buffer) must return the RAW STREAM, not a render.
	autoRes := invoke("read", `{"id":`+id+`,"mode":"auto"}`)
	var auto struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(autoRes), &auto); err != nil {
		t.Fatalf("read mode:auto unmarshal: %v (raw %q)", err, autoRes)
	}
	if auto.Kind != "stream" {
		t.Errorf("read mode:auto on a non-full-screen program: kind = %q, want stream\n%s", auto.Kind, autoRes)
	}

	// 7. Drive the session onto the ALTERNATE SCREEN BUFFER (DECSET 1049 —
	//    what vim/htop/less emit) and confirm mode:auto now flips to the
	//    rendered screen. cat echoes the bytes back to its stdout, so the
	//    PTY's vt10x emulator processes the escape and sets ModeAltScreen.
	//    This exercises the AltScreen=TRUE branch — the feature's core that
	//    the stream-only assertion above can't reach.
	// \u001b is the ESC byte; the trailing newline flushes cat's
	// canonical-mode line buffer so the real escape sequence (not just
	// the kernel's ^[ echo of control bytes) reaches stdout.
	invoke("write", `{"id":`+id+`,"data":"\u001b[?1049h\n"}`)
	deadline = time.Now().Add(2 * time.Second)
	var altKind string
	for time.Now().Before(deadline) {
		var a struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal([]byte(invoke("read", `{"id":`+id+`,"mode":"auto"}`)), &a)
		altKind = a.Kind
		if altKind == "screen" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if altKind != "screen" {
		t.Errorf("read mode:auto after entering the alternate screen buffer: kind = %q, want screen", altKind)
	}

	// 7b. Auto-screen drains the raw ring (Codex P2): while on the alt
	//     screen, write a canary and confirm it lands (via a non-draining
	//     mode:screen peek). One mode:auto read then renders the screen AND
	//     discards the raw backlog, so a full-screen program's escape
	//     stream can't resurface in a later read. The byte stream feeds
	//     the ring before the grid, so a rendered canary is a buffered
	//     one; after the drain an explicit mode:stream read must not see
	//     it. Guards the auto-mode backlog leak.
	const canary = "DRAIN_CANARY"
	invoke("write", `{"id":`+id+`,"data":"`+canary+`\n"}`)
	deadline = time.Now().Add(2 * time.Second)
	canarySeen := false
	for time.Now().Before(deadline) {
		if strings.Contains(invoke("read", `{"id":`+id+`,"mode":"screen"}`), canary) {
			canarySeen = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !canarySeen {
		t.Fatal("canary never rendered on the alt screen")
	}
	// Draining auto read (alt-screen active → screen kind → discards ring).
	invoke("read", `{"id":`+id+`,"mode":"auto"}`)
	// Explicit stream read consumes the ring; the drained canary must be gone.
	var streamRes struct {
		DataB64 string `json:"data_b64"`
	}
	_ = json.Unmarshal([]byte(invoke("read", `{"id":`+id+`,"mode":"stream","timeout_ms":200}`)), &streamRes)
	dec, _ := base64.StdEncoding.DecodeString(streamRes.DataB64)
	if strings.Contains(string(dec), canary) {
		t.Errorf("auto-screen read did not drain the ring; canary leaked into the stream:\n%q", string(dec))
	}

	// 8. EOF path (regression guard for the -1 sentinel mishandling): spawn
	//    a short-lived process, drain its output, then keep reading until the
	//    closed+empty session surfaces a clean {"kind":"stream","eof":true}.
	//    The bug was the guest decoding the host's -1 EOF as a 1-byte error
	//    string read from the zeroed scratch buffer ({"error":"read: \x00"}).
	eofOut := invoke("spawn", `{"argv":["/bin/sh","-c","printf bye"]}`)
	var eofSpawn struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal([]byte(eofOut), &eofSpawn); err != nil {
		t.Fatalf("eof spawn unmarshal: %v (raw %q)", err, eofOut)
	}
	eofID := eofSpawn.ID.String()
	defer invoke("destroy", `{"id":`+eofID+`}`)

	deadline = time.Now().Add(3 * time.Second)
	sawEOF := false
	for time.Now().Before(deadline) {
		var r struct {
			Kind string `json:"kind"`
			EOF  bool   `json:"eof"`
		}
		raw := invoke("read", `{"id":`+eofID+`,"mode":"stream","timeout_ms":200}`)
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("eof read unmarshal: %v (raw %q)", err, raw)
		}
		if r.EOF {
			if r.Kind != "stream" {
				t.Errorf("EOF read kind = %q, want stream (raw %q)", r.Kind, raw)
			}
			sawEOF = true
			break
		}
	}
	if !sawEOF {
		t.Error("read mode:stream on a closed+drained session never reported eof:true")
	}
}
