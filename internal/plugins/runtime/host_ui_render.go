package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// renderRequestWire is the JSON shape plugins send via stado_ui_render.
// Mirrors the public Panel + Section types but uses lowercase JSON
// field names. Each section's body field set is validated against the
// declared Kind at decode so renderers don't have to guess.
//
// Spec: F9b (.agent/specs/open/f9b-ui-render.md). F9b.1 (2026-05-09).
type renderRequestWire struct {
	Title    string             `json:"title"`
	Sections []sectionWire      `json:"sections"`
	Variant  string             `json:"variant,omitempty"`
	ID       string             `json:"id,omitempty"`
	Footer   string             `json:"footer,omitempty"`
	// reserved for forward-compat: when adding optional metadata,
	// extend renderRequestWire here, not via untagged map["any"].
	_ struct{} `json:"-"`
}

type sectionWire struct {
	Kind    string         `json:"kind"`
	Heading string         `json:"heading,omitempty"`
	Text    string         `json:"text,omitempty"`
	KV      []kvPairWire   `json:"kv,omitempty"`
	List    *listBodyWire  `json:"list,omitempty"`
	Code    *codeBodyWire  `json:"code,omitempty"`
	Table   *tableBodyWire `json:"table,omitempty"`
	Diff    *diffBodyWire  `json:"diff,omitempty"`
}

type kvPairWire struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type listBodyWire struct {
	Marker string   `json:"marker,omitempty"`
	Items  []string `json:"items"`
}

type codeBodyWire struct {
	Language string `json:"language,omitempty"`
	Content  string `json:"content"`
}

type tableBodyWire struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

type diffBodyWire struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// validRenderVariants is the closed enum for Panel.Variant. Empty
// string is allowed (renderers treat as "info"); explicit values are
// validated at decode so an unrecognised variant can't silently slip
// through to renderer-side code that switches on it. F9b.1.
var validRenderVariants = map[string]bool{
	"":               true,
	"info":           true,
	"ok":             true,
	"warn":           true,
	"error":          true,
	"recommendation": true,
}

// validRenderListMarkers is the closed enum for ListBody.Marker.
// Empty string defaults to "bullet". F9b.1.
var validRenderListMarkers = map[string]bool{
	"":         true,
	"bullet":   true,
	"numbered": true,
	"check":    true,
}

// validRenderSectionKinds is the closed enum for Section.Kind. F9b.1.
var validRenderSectionKinds = map[string]bool{
	"text":  true,
	"kv":    true,
	"list":  true,
	"code":  true,
	"table": true,
	"diff":  true,
}

// registerUIRenderImport wires stado_ui_render. Wire format:
//
//	stado_ui_render(req_ptr, req_len, err_ptr, err_cap) -> int32
//
// Returns 0 on success (panel emitted, fire-and-forget). Negative
// values use the encodeToolSidePayload convention: -n means an
// error message of n bytes is at err_ptr.
//
// Cap-gated by ui:render. Routes to host.RenderBridge; nil bridge =
// drop on the floor with success (per F9 spec — a render on a
// disconnected channel should not error). Errors only on
// shape / size violations and explicit bridge rejections.
//
// F9b.1 (host scaffolding only): the import + cap check + decode +
// validation land here; per-channel renderers (TUI, ACP, MCP,
// headless) wire RenderBridge implementations in subsequent F9b
// phases. Until then RenderBridge is nil on every channel and emits
// silently succeed.
func registerUIRenderImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			reqPtr := api.DecodeU32(stack[0])
			reqLen := api.DecodeU32(stack[1])
			errPtr := api.DecodeU32(stack[2])
			errCap := api.DecodeU32(stack[3])

			fail := func(msg string) {
				stack[0] = api.EncodeI32(encodeToolSidePayload(mod, errPtr, errCap, []byte(msg)))
			}

			if !host.UIRender {
				host.Logger.Warn("stado_ui_render denied — manifest lacks ui:render")
				fail("ui:render cap missing")
				return
			}
			if reqLen > maxPluginRuntimeUIRenderRequestBytes {
				fail(fmt.Sprintf("request payload exceeds %d bytes (cap)", maxPluginRuntimeUIRenderRequestBytes))
				return
			}
			payload, err := readBytesLimited(mod, reqPtr, reqLen, maxPluginRuntimeUIRenderRequestBytes)
			if err != nil {
				fail("request memory read failed")
				return
			}
			var wire renderRequestWire
			if err := json.Unmarshal(payload, &wire); err != nil {
				fail("request JSON decode: " + err.Error())
				return
			}
			panel, err := decodeRenderRequest(wire)
			if err != nil {
				fail(err.Error())
				return
			}

			// nil bridge = drop on the floor (success). The plugin
			// can't observe whether a render channel is wired; this
			// matches the F9 spec's "if channel disconnected, emit
			// succeeds silently" rule and the F9a print precedent.
			if host.RenderBridge == nil {
				stack[0] = api.EncodeI32(0)
				return
			}

			if err := host.RenderBridge.Render(ctx, panel); err != nil {
				host.Logger.Warn("stado_ui_render bridge failed", "err", err)
				fail("render rejected: " + err.Error())
				return
			}
			stack[0] = api.EncodeI32(0)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32}).
		Export("stado_ui_render")
}

// decodeRenderRequest validates the wire payload + applies per-spec
// limits (title / footer / heading / id strings; variant in the
// fixed set; list marker in the fixed set; section bodies match
// declared kind; per-section size cap; table row × column caps).
// Centralised so the host import body stays tight and validation
// is unit-testable. F9b.1.
func decodeRenderRequest(w renderRequestWire) (Panel, error) {
	if w.Title == "" {
		return Panel{}, fmt.Errorf("title required")
	}
	if len(w.Title) > maxPluginRuntimeUIRenderTitleBytes {
		return Panel{}, fmt.Errorf("title exceeds %d bytes", maxPluginRuntimeUIRenderTitleBytes)
	}
	if !validRenderVariants[w.Variant] {
		return Panel{}, fmt.Errorf("variant %q not in {info,ok,warn,error,recommendation}", w.Variant)
	}
	if len(w.ID) > maxPluginRuntimeUIRenderIDBytes {
		return Panel{}, fmt.Errorf("id exceeds %d bytes", maxPluginRuntimeUIRenderIDBytes)
	}
	if len(w.Footer) > maxPluginRuntimeUIRenderFooterBytes {
		return Panel{}, fmt.Errorf("footer exceeds %d bytes", maxPluginRuntimeUIRenderFooterBytes)
	}
	if len(w.Sections) == 0 {
		return Panel{}, fmt.Errorf("at least one section required")
	}

	out := Panel{
		Title:    w.Title,
		Variant:  w.Variant,
		ID:       w.ID,
		Footer:   w.Footer,
		Sections: make([]Section, 0, len(w.Sections)),
	}
	for i, sw := range w.Sections {
		sec, err := decodeSection(i, sw)
		if err != nil {
			return Panel{}, err
		}
		out.Sections = append(out.Sections, sec)
	}
	return out, nil
}

// decodeSection validates one wire section against its declared
// kind. The "exactly one body field per Kind" invariant is enforced
// here so renderers downstream can switch on Kind without defensive
// fallthrough. F9b.1.
func decodeSection(i int, sw sectionWire) (Section, error) {
	if !validRenderSectionKinds[sw.Kind] {
		return Section{}, fmt.Errorf("section %d: kind %q not in {text,kv,list,code,table,diff}", i, sw.Kind)
	}
	if len(sw.Heading) > maxPluginRuntimeUIRenderHeadingBytes {
		return Section{}, fmt.Errorf("section %d: heading exceeds %d bytes", i, maxPluginRuntimeUIRenderHeadingBytes)
	}

	// Per-section approximate size enforcement: encode just this
	// section back to JSON and compare against the per-section cap.
	// Cheaper than tracking budget across the recursive walk and
	// matches the spec's "per section after JSON-decode" wording.
	sb, _ := json.Marshal(sw)
	if uint32(len(sb)) > maxPluginRuntimeUIRenderSectionBytes {
		return Section{}, fmt.Errorf("section %d: serialised size %d exceeds %d bytes (cap)",
			i, len(sb), maxPluginRuntimeUIRenderSectionBytes)
	}

	sec := Section{Kind: sw.Kind, Heading: sw.Heading}
	switch sw.Kind {
	case "text":
		if err := requireOnlyBody(i, sw, "text"); err != nil {
			return Section{}, err
		}
		sec.Text = sw.Text
	case "kv":
		if err := requireOnlyBody(i, sw, "kv"); err != nil {
			return Section{}, err
		}
		if len(sw.KV) == 0 {
			return Section{}, fmt.Errorf("section %d: kv body requires at least one pair", i)
		}
		for j, p := range sw.KV {
			if len(p.Label) > maxPluginRuntimeUIRenderKVLabelBytes {
				return Section{}, fmt.Errorf("section %d: kv pair %d label exceeds %d bytes", i, j, maxPluginRuntimeUIRenderKVLabelBytes)
			}
			if len(p.Value) > maxPluginRuntimeUIRenderKVValueBytes {
				return Section{}, fmt.Errorf("section %d: kv pair %d value exceeds %d bytes", i, j, maxPluginRuntimeUIRenderKVValueBytes)
			}
			sec.KV = append(sec.KV, KVPair(p))
		}
	case "list":
		if err := requireOnlyBody(i, sw, "list"); err != nil {
			return Section{}, err
		}
		if sw.List == nil {
			return Section{}, fmt.Errorf("section %d: list body required", i)
		}
		if !validRenderListMarkers[sw.List.Marker] {
			return Section{}, fmt.Errorf("section %d: list marker %q not in {bullet,numbered,check}", i, sw.List.Marker)
		}
		if len(sw.List.Items) == 0 {
			return Section{}, fmt.Errorf("section %d: list body requires at least one item", i)
		}
		sec.List = ListBody{Marker: sw.List.Marker, Items: append([]string(nil), sw.List.Items...)}
	case "code":
		if err := requireOnlyBody(i, sw, "code"); err != nil {
			return Section{}, err
		}
		if sw.Code == nil {
			return Section{}, fmt.Errorf("section %d: code body required", i)
		}
		sec.Code = CodeBody{Language: sw.Code.Language, Content: sw.Code.Content}
	case "table":
		if err := requireOnlyBody(i, sw, "table"); err != nil {
			return Section{}, err
		}
		if sw.Table == nil {
			return Section{}, fmt.Errorf("section %d: table body required", i)
		}
		if len(sw.Table.Columns) == 0 {
			return Section{}, fmt.Errorf("section %d: table requires at least one column", i)
		}
		if len(sw.Table.Columns) > maxPluginRuntimeUIRenderTableCols {
			return Section{}, fmt.Errorf("section %d: table has %d columns (max %d)",
				i, len(sw.Table.Columns), maxPluginRuntimeUIRenderTableCols)
		}
		if len(sw.Table.Rows) > maxPluginRuntimeUIRenderTableRows {
			return Section{}, fmt.Errorf("section %d: table has %d rows (max %d)",
				i, len(sw.Table.Rows), maxPluginRuntimeUIRenderTableRows)
		}
		for r, row := range sw.Table.Rows {
			if len(row) != len(sw.Table.Columns) {
				return Section{}, fmt.Errorf("section %d: table row %d has %d cells, want %d",
					i, r, len(row), len(sw.Table.Columns))
			}
		}
		sec.Table = TableBody{
			Columns: append([]string(nil), sw.Table.Columns...),
			Rows:    cloneRows(sw.Table.Rows),
		}
	case "diff":
		if err := requireOnlyBody(i, sw, "diff"); err != nil {
			return Section{}, err
		}
		if sw.Diff == nil {
			return Section{}, fmt.Errorf("section %d: diff body required", i)
		}
		sec.Diff = DiffBody{Before: sw.Diff.Before, After: sw.Diff.After}
	}
	return sec, nil
}

// requireOnlyBody enforces "exactly one body field set per declared
// Kind." Returns an error if any sibling body field is populated.
// Empty Heading is fine — Heading is a section-level decoration,
// not a body. F9b.1.
func requireOnlyBody(i int, sw sectionWire, want string) error {
	others := map[string]bool{
		"text":  sw.Text != "",
		"kv":    len(sw.KV) > 0,
		"list":  sw.List != nil,
		"code":  sw.Code != nil,
		"table": sw.Table != nil,
		"diff":  sw.Diff != nil,
	}
	for name, set := range others {
		if name == want {
			continue
		}
		if set {
			return fmt.Errorf("section %d: kind=%q must not also carry %q body", i, want, name)
		}
	}
	return nil
}

// cloneRows is a defensive deep-copy so the bridge implementation
// can retain the Panel beyond the wasm invocation frame without
// risk of in-place mutation by the caller.
func cloneRows(in [][]string) [][]string {
	if in == nil {
		return nil
	}
	out := make([][]string, len(in))
	for i, row := range in {
		out[i] = append([]string(nil), row...)
	}
	return out
}
