package runtime

import (
	"fmt"
	"strings"
	"testing"
)

// F9b.1 unit-tests for stado_ui_render's wire decoder. The host
// import + bridge dispatch are exercised separately via the
// runtime-instantiation tests; the decode logic gets a focused
// table-driven sweep here so future changes don't regress the
// validation contract documented in
// .agent/specs/open/f9b-ui-render.md.

// TestDecodeRenderRequest_TextSection: the simplest valid panel —
// one text section — decodes into a populated Panel.
func TestDecodeRenderRequest_TextSection(t *testing.T) {
	w := renderRequestWire{
		Title:   "Scan results",
		Variant: "ok",
		Sections: []sectionWire{
			{Kind: "text", Heading: "Summary", Text: "3 hosts up, 2 down."},
		},
	}
	p, err := decodeRenderRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Title != "Scan results" || p.Variant != "ok" {
		t.Errorf("Panel = %+v", p)
	}
	if len(p.Sections) != 1 || p.Sections[0].Kind != "text" || p.Sections[0].Text != "3 hosts up, 2 down." {
		t.Errorf("Sections = %+v", p.Sections)
	}
}

// TestDecodeRenderRequest_Target exercises the #21-part-2 display-target
// enum: valid values, empty→viewport normalisation, and the id-required
// rule for the addressable sidebar/footer targets.
func TestDecodeRenderRequest_Target(t *testing.T) {
	base := []sectionWire{{Kind: "text", Text: "body"}}
	cases := []struct {
		name       string
		target     string
		id         string
		wantTarget string
		wantErr    string
	}{
		{name: "empty normalises to viewport", target: "", wantTarget: "viewport"},
		{name: "explicit viewport", target: "viewport", wantTarget: "viewport"},
		{name: "sidebar with id", target: "sidebar", id: "scan", wantTarget: "sidebar"},
		{name: "footer with id", target: "footer", id: "creds", wantTarget: "footer"},
		{name: "log ignores id", target: "log", wantTarget: "log"},
		{name: "sidebar without id rejected", target: "sidebar", wantErr: "requires a non-empty id"},
		{name: "footer without id rejected", target: "footer", wantErr: "requires a non-empty id"},
		{name: "all-control-char id rejected", target: "sidebar", id: "\x01\x02", wantErr: "requires a non-empty id"},
		{name: "unknown target rejected", target: "popup", id: "x", wantErr: "not in {viewport"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := decodeRenderRequest(renderRequestWire{
				Title: "t", Target: tc.target, ID: tc.id, Sections: base,
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if p.Target != tc.wantTarget {
				t.Errorf("Target = %q, want %q", p.Target, tc.wantTarget)
			}
		})
	}
}

// TestDecodeRenderRequest_AllBodyKinds: sweep every supported body
// kind so adding a new kind in the future requires updating this
// test (it's the canary for the body-shape contract).
func TestDecodeRenderRequest_AllBodyKinds(t *testing.T) {
	w := renderRequestWire{
		Title: "Body kind sweep",
		Sections: []sectionWire{
			{Kind: "text", Text: "plain"},
			{Kind: "kv", KV: []kvPairWire{{Label: "host", Value: "10.0.0.1"}}},
			{Kind: "list", List: &listBodyWire{Marker: "numbered", Items: []string{"one", "two"}}},
			{Kind: "code", Code: &codeBodyWire{Language: "go", Content: "fmt.Println()"}},
			{Kind: "table", Table: &tableBodyWire{
				Columns: []string{"host", "port"},
				Rows:    [][]string{{"10.0.0.1", "22"}, {"10.0.0.2", "80"}},
			}},
			{Kind: "diff", Diff: &diffBodyWire{Before: "old", After: "new"}},
		},
	}
	p, err := decodeRenderRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(p.Sections) != 6 {
		t.Fatalf("got %d sections, want 6", len(p.Sections))
	}
	if p.Sections[1].KV[0].Value != "10.0.0.1" {
		t.Errorf("kv body lost data: %+v", p.Sections[1].KV)
	}
	if p.Sections[3].Code.Language != "go" {
		t.Errorf("code body lost language: %+v", p.Sections[3].Code)
	}
	if len(p.Sections[4].Table.Rows) != 2 || p.Sections[4].Table.Rows[1][1] != "80" {
		t.Errorf("table body lost rows: %+v", p.Sections[4].Table)
	}
	if p.Sections[5].Diff.After != "new" {
		t.Errorf("diff body lost After: %+v", p.Sections[5].Diff)
	}
}

// TestDecodeRenderRequest_RejectsBadInputs covers the validation
// surface — every error path must fire with a substring an operator
// can map back to the cap that was hit. Spec ACs 3 and 4.
func TestDecodeRenderRequest_RejectsBadInputs(t *testing.T) {
	textSec := sectionWire{Kind: "text", Text: "x"}
	cases := []struct {
		name, want string
		wire       renderRequestWire
	}{
		{
			name: "empty title",
			wire: renderRequestWire{Sections: []sectionWire{textSec}},
			want: "title required",
		},
		{
			name: "all-control-char title scrubs to empty",
			wire: renderRequestWire{Title: "\x01\x02", Sections: []sectionWire{textSec}},
			want: "title required",
		},
		{
			name: "title too long",
			wire: renderRequestWire{
				Title:    strings.Repeat("T", maxPluginRuntimeUIRenderTitleBytes+1),
				Sections: []sectionWire{textSec},
			},
			want: "title exceeds",
		},
		{
			name: "unknown variant",
			wire: renderRequestWire{Title: "t", Variant: "fatal", Sections: []sectionWire{textSec}},
			want: "variant",
		},
		{
			name: "footer too long",
			wire: renderRequestWire{
				Title:    "t",
				Footer:   strings.Repeat("f", maxPluginRuntimeUIRenderFooterBytes+1),
				Sections: []sectionWire{textSec},
			},
			want: "footer exceeds",
		},
		{
			name: "id too long",
			wire: renderRequestWire{
				Title:    "t",
				ID:       strings.Repeat("i", maxPluginRuntimeUIRenderIDBytes+1),
				Sections: []sectionWire{textSec},
			},
			want: "id exceeds",
		},
		{
			name: "no sections",
			wire: renderRequestWire{Title: "t"},
			want: "at least one section",
		},
		{
			name: "unknown section kind",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{{Kind: "blob"}}},
			want: "kind",
		},
		{
			name: "kind=text + foreign body field set",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "text", Text: "hi", Code: &codeBodyWire{Content: "x"}},
			}},
			want: "must not also carry",
		},
		{
			name: "kind=kv with no pairs",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{{Kind: "kv"}}},
			want: "kv body requires",
		},
		{
			name: "kind=list with bad marker",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "list", List: &listBodyWire{Marker: "dash", Items: []string{"a"}}},
			}},
			want: "list marker",
		},
		{
			name: "kind=table with mismatched row width",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "table", Table: &tableBodyWire{
					Columns: []string{"a", "b"},
					Rows:    [][]string{{"x"}},
				}},
			}},
			want: "cells, want",
		},
		{
			name: "kind=table over row cap",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "table", Table: &tableBodyWire{
					Columns: []string{"a"},
					Rows:    makeRows(maxPluginRuntimeUIRenderTableRows+1, 1),
				}},
			}},
			want: "rows (max",
		},
		{
			name: "kind=table over column cap",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "table", Table: &tableBodyWire{
					Columns: makeRow(maxPluginRuntimeUIRenderTableCols + 1),
					Rows:    [][]string{makeRow(maxPluginRuntimeUIRenderTableCols + 1)},
				}},
			}},
			want: "columns (max",
		},
		{
			name: "kv pair value too long",
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "kv", KV: []kvPairWire{
					{Label: "k", Value: strings.Repeat("v", maxPluginRuntimeUIRenderKVValueBytes+1)},
				}},
			}},
			want: "kv pair 0 value exceeds",
		},
		{
			name: "section over per-section cap",
			// Big text body forces the per-section JSON re-encode
			// to exceed the 32 KiB ceiling.
			wire: renderRequestWire{Title: "t", Sections: []sectionWire{
				{Kind: "text", Text: strings.Repeat("x", int(maxPluginRuntimeUIRenderSectionBytes)+1)},
			}},
			want: "exceeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeRenderRequest(tc.wire)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestDecodeRenderRequest_DefensiveCopy: the decoded Panel must own
// its own slice memory so a bridge can retain it past the wasm
// invocation frame without risk of in-place mutation by the caller
// (which on the actual host path is the JSON-decoder's underlying
// buffer).
func TestDecodeRenderRequest_DefensiveCopy(t *testing.T) {
	w := renderRequestWire{
		Title: "t",
		Sections: []sectionWire{
			{Kind: "list", List: &listBodyWire{Items: []string{"a", "b"}}},
			{Kind: "table", Table: &tableBodyWire{
				Columns: []string{"x"},
				Rows:    [][]string{{"1"}, {"2"}},
			}},
		},
	}
	p, err := decodeRenderRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Mutate the wire payload after decode; the Panel must not see
	// the change.
	w.Sections[0].List.Items[0] = "MUTATED"
	w.Sections[1].Table.Rows[0][0] = "MUTATED"
	if p.Sections[0].List.Items[0] == "MUTATED" {
		t.Error("Panel.List shared memory with wire payload")
	}
	if p.Sections[1].Table.Rows[0][0] == "MUTATED" {
		t.Error("Panel.Table shared memory with wire payload")
	}
}

// TestRenderBridge_NilSucceedsSilently: the host scaffolding ships
// before any per-channel renderer is wired (F9b.2-5). Until then
// every Host has RenderBridge=nil; the import path must succeed
// silently per the F9 spec's "if channel disconnected, emit
// succeeds silently" rule. Asserted at the dispatcher level via
// the ApplyToolFilter wiring rather than through the wasm boundary
// (the boundary tests live alongside other host-import bridge
// suites).
//
// This test asserts the contract on the bridge interface: nil is a
// valid RenderBridge meaning "drop on the floor."
func TestRenderBridge_NilIsValid(t *testing.T) {
	var rb RenderBridge
	if rb != nil {
		t.Fatal("zero-value RenderBridge should be nil")
	}
	// The dispatch site (registerUIRenderImport) checks nil before
	// calling Render — exercised via the bridge-test suites in
	// later F9b phases. This test pins the type-level contract.
}

// makeRow returns a length-n []string with placeholder values so
// table-cap tests don't drag in 16+ literal strings each.
func makeRow(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "x"
	}
	return out
}

// makeRows returns rows×cols []string slices for the table-cap
// boundary test. Avoids per-cell allocation churn cluttering the
// table-driven case bodies.
func makeRows(rows, cols int) [][]string {
	out := make([][]string, rows)
	for i := range out {
		out[i] = makeRow(cols)
	}
	return out
}

// Codex C2/J-c P1 regression: plugin-supplied strings reaching the
// stado_ui_render decoder are the wasm-host trust boundary. Pre-fix
// every string field flowed through to the Panel struct verbatim,
// so a malicious / buggy plugin could plant OSC52 (clipboard hijack),
// OSC8 (clickable link), or CSI escapes in Title / Footer / Heading
// and the renderer at panel_render.go would emit them to the
// operator's terminal unchecked. After fix the decoder sanitizes at
// the boundary using StripControlChars for header/cell zones and
// SanitizeForTerminal for prose body zones.
func TestDecodeRenderRequest_SanitizesEveryStringField(t *testing.T) {
	// Trio of representative escapes. ESC = 0x1b, BEL = 0x07.
	const osc52 = "\x1b]52;c;evil\x07"
	const osc8 = "\x1b]8;;https://evil\x1b\\"
	const csi = "\x1b[2K\x1b[1;1H"
	bad := osc52 + osc8 + csi

	w := renderRequestWire{
		Title:   "Title" + bad,
		Variant: "info",
		ID:      "id-" + bad,
		Footer:  "footer-" + bad,
		Sections: []sectionWire{
			{
				Kind:    "text",
				Heading: "Heading" + bad,
				Text:    "prose with\nnewlines and " + bad,
			},
			{
				Kind:    "kv",
				Heading: "Pairs",
				KV: []kvPairWire{
					{Label: "key-" + bad, Value: "value-" + bad},
				},
			},
			{
				Kind:    "list",
				Heading: "Items",
				List: &listBodyWire{
					Marker: "bullet",
					Items:  []string{"item-" + bad, "item2"},
				},
			},
			{
				Kind:    "code",
				Heading: "Snippet",
				Code: &codeBodyWire{
					Language: "go-" + bad,
					Content:  "func main() {\n  // " + bad + "\n}",
				},
			},
			{
				Kind:    "table",
				Heading: "Data",
				Table: &tableBodyWire{
					Columns: []string{"col-" + bad, "col2"},
					Rows:    [][]string{{"cell-" + bad, "cell2"}},
				},
			},
			{
				Kind:    "diff",
				Heading: "Change",
				Diff: &diffBodyWire{
					Before: "before\n" + bad,
					After:  "after\n" + bad,
				},
			},
		},
	}

	p, err := decodeRenderRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Helper: every escape we planted must be gone from the field.
	assertClean := func(name, got string) {
		for _, esc := range []string{"\x1b", "\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
			if strings.Contains(got, esc) {
				t.Errorf("%s leaks %q: %q", name, esc, got)
			}
		}
	}

	assertClean("Panel.Title", p.Title)
	assertClean("Panel.ID", p.ID)
	assertClean("Panel.Footer", p.Footer)
	for i, sec := range p.Sections {
		assertClean(fmt.Sprintf("Section[%d].Heading", i), sec.Heading)
		switch sec.Kind {
		case "text":
			assertClean("Section[0].Text", sec.Text)
			// SanitizeForTerminal preserves \n in prose bodies.
			if !strings.Contains(sec.Text, "prose with\nnewlines") {
				t.Errorf("text section should preserve legitimate \\n; got %q", sec.Text)
			}
		case "kv":
			assertClean("Section[1].KV[0].Label", sec.KV[0].Label)
			assertClean("Section[1].KV[0].Value", sec.KV[0].Value)
		case "list":
			for j, it := range sec.List.Items {
				assertClean(fmt.Sprintf("Section[2].List.Items[%d]", j), it)
			}
		case "code":
			assertClean("Section[3].Code.Language", sec.Code.Language)
			assertClean("Section[3].Code.Content", sec.Code.Content)
		case "table":
			for j, c := range sec.Table.Columns {
				assertClean(fmt.Sprintf("Section[4].Table.Columns[%d]", j), c)
			}
			for r, row := range sec.Table.Rows {
				for c, cell := range row {
					assertClean(fmt.Sprintf("Section[4].Table.Rows[%d][%d]", r, c), cell)
				}
			}
		case "diff":
			assertClean("Section[5].Diff.Before", sec.Diff.Before)
			assertClean("Section[5].Diff.After", sec.Diff.After)
		}
	}

	// Single-line zones must NOT preserve any \n (a planted newline
	// in Title / Heading / cell would break row layout); the test
	// payloads above don't include literal \n in single-line zones
	// but the implementation's StripControlChars guarantees this.
	// Spot-check one cell:
	for _, c := range p.Sections[4].Table.Columns {
		if strings.Contains(c, "\n") {
			t.Errorf("table column header leaked a newline: %q", c)
		}
	}
}
