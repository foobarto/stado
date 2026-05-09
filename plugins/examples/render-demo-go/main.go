package main

import (
	"encoding/json"
	"strconv"
	"sync"
	"unsafe"
)

// render-demo-go — minimal example exercising the ui:render
// capability and the stado_ui_render host primitive end-to-end.
// Mirrors choose-demo-go's shape so plugin authors can copy-paste-
// modify the boilerplate. F9b.6.

func main() {}

//go:wasmimport stado stado_ui_render
func stadoUIRender(reqPtr, reqLen, errPtr, errCap uint32) int32

var pinned sync.Map

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) {
	pinned.Delete(uintptr(ptr))
	_ = size
}

// ── Wire types — same shape decoded by
//    internal/plugins/runtime/host_ui_render.go::renderRequestWire.
//    Each section sets exactly one body field for its declared kind;
//    the host validates that invariant at decode and returns -n with
//    a message when violated.

type kvPair struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type listBody struct {
	Marker string   `json:"marker,omitempty"`
	Items  []string `json:"items"`
}

type codeBody struct {
	Language string `json:"language,omitempty"`
	Content  string `json:"content"`
}

type tableBody struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

type diffBody struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type section struct {
	Kind    string     `json:"kind"`
	Heading string     `json:"heading,omitempty"`
	Text    string     `json:"text,omitempty"`
	KV      []kvPair   `json:"kv,omitempty"`
	List    *listBody  `json:"list,omitempty"`
	Code    *codeBody  `json:"code,omitempty"`
	Table   *tableBody `json:"table,omitempty"`
	Diff    *diffBody  `json:"diff,omitempty"`
}

type panelEnvelope struct {
	Title    string    `json:"title"`
	Sections []section `json:"sections"`
	Variant  string    `json:"variant,omitempty"`
	ID       string    `json:"id,omitempty"`
	Footer   string    `json:"footer,omitempty"`
}

//go:wasmexport stado_tool_render_demo
func stadoToolRenderDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var args struct {
		Variant string `json:"variant"` // optional override
	}
	if raw := wasmBytes(argsPtr, argsLen); len(raw) > 0 {
		// Tolerate empty / partial args; this is a demo tool.
		_ = json.Unmarshal(raw, &args)
	}
	variant := args.Variant
	if variant == "" {
		variant = "ok"
	}

	// Cycle through every body kind so a single invocation visibly
	// exercises the renderer end-to-end on whichever channel the
	// operator is on (TUI / ACP / MCP / headless).
	panel := panelEnvelope{
		Title:   "render_demo: all body kinds",
		Variant: variant,
		Footer:  "render-demo-go: stado_ui_render smoke",
		Sections: []section{
			{Kind: "text", Heading: "Plain text",
				Text: "This is a paragraph body. Long lines wrap " +
					"on word boundaries inside the panel. Markdown " +
					"is reserved as a future enhancement; for now " +
					"text is rendered verbatim with wrapping."},
			{Kind: "kv", Heading: "Key/value pairs",
				KV: []kvPair{
					{Label: "host", Value: "10.0.0.1"},
					{Label: "service", Value: "ssh (OpenSSH 9.6p1)"},
					{Label: "port", Value: "22"},
				}},
			{Kind: "list", Heading: "Numbered list",
				List: &listBody{Marker: "numbered", Items: []string{
					"First step",
					"Second step",
					"Third step",
				}}},
			{Kind: "list", Heading: "Bullet list",
				List: &listBody{Items: []string{
					"alpha",
					"beta",
					"gamma",
				}}},
			{Kind: "list", Heading: "Checklist",
				List: &listBody{Marker: "check", Items: []string{
					"Reproduce the bug",
					"Identify the regression range",
					"Write the regression test",
				}}},
			{Kind: "code", Heading: "Code (language hint)",
				Code: &codeBody{Language: "go", Content: "fmt.Println(\"hello, render\")"}},
			{Kind: "table", Heading: "Table",
				Table: &tableBody{
					Columns: []string{"host", "port", "service"},
					Rows: [][]string{
						{"10.0.0.1", "22", "ssh"},
						{"10.0.0.7", "80", "http"},
						{"10.0.0.42", "443", "https"},
					},
				}},
			{Kind: "diff", Heading: "Diff",
				Diff: &diffBody{
					Before: "old behaviour",
					After:  "new behaviour",
				}},
		},
	}

	req, err := json.Marshal(panel)
	if err != nil {
		return writeError(resultPtr, resultCap, "render_demo: marshal: "+err.Error())
	}

	// stado_ui_render is fire-and-forget. On success it returns 0;
	// on failure it returns -n where the host has written n bytes
	// of error message into errPtr — read it back so the operator
	// sees the real reason (cap denied / size violation / bridge
	// rejection). This is the canonical pattern; same shape every
	// other host import in this codebase uses.
	reqPtr := stadoAlloc(int32(len(req)))
	defer stadoFree(reqPtr, int32(len(req)))
	dst := wasmBytes(reqPtr, int32(len(req)))
	copy(dst, req)

	// Use the result buffer to receive any error bytes — same buffer
	// the operator-visible result eventually lands in. resultCap
	// gives the host its upper bound.
	n := stadoUIRender(uint32(reqPtr), uint32(len(req)), uint32(resultPtr), uint32(resultCap))
	if n < 0 {
		// Host already wrote -n bytes of error message at resultPtr;
		// turn it into our own visible failure envelope.
		errLen := -int(n)
		if errLen > int(resultCap) {
			errLen = int(resultCap)
		}
		errMsg := string(wasmBytes(resultPtr, int32(errLen)))
		return writeError(resultPtr, resultCap, "render_demo: stado_ui_render: "+errMsg)
	}
	return writeResult(resultPtr, resultCap,
		"render_demo: panel emitted ("+strconv.Itoa(len(panel.Sections))+" sections)")
}

func wasmBytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(size))
}

func writeResult(resultPtr, resultCap int32, msg string) int32 {
	if resultCap <= 0 {
		return 0
	}
	dst := wasmBytes(resultPtr, resultCap)
	if len(dst) == 0 {
		return 0
	}
	payload := []byte(msg)
	if len(payload) > len(dst) {
		payload = payload[:len(dst)]
	}
	copy(dst, payload)
	return int32(len(payload))
}

func writeError(resultPtr, resultCap int32, msg string) int32 {
	n := writeResult(resultPtr, resultCap, msg)
	if n <= 0 {
		return -1
	}
	return -n
}
