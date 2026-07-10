package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_ui_render
func stadoUIRender(reqPtr, reqLen, errPtr, errCap uint32) int32

//go:wasmimport stado stado_ui_approve
func stadoUIApprove(titlePtr, titleLen, bodyPtr, bodyLen uint32) int32

//go:wasmimport stado stado_ui_choose
func stadoUIChoose(reqPtr, reqLen, respPtr, respCap uint32) int32

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
	Footer   string    `json:"footer,omitempty"`
}

//go:wasmexport stado_tool_render_demo
func stadoToolRenderDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	_ = argsPtr
	_ = argsLen
	panel := panelEnvelope{
		Title:   "render_demo: all body kinds",
		Variant: "ok",
		Footer:  "stado UAT render fixture",
		Sections: []section{
			{Kind: "text", Heading: "Plain text", Text: "A paragraph that proves wrapped text reaches the terminal renderer."},
			{Kind: "kv", Heading: "Key/value pairs", KV: []kvPair{{Label: "host", Value: "10.0.0.1"}, {Label: "port", Value: "22"}}},
			{Kind: "list", Heading: "Numbered list", List: &listBody{Marker: "numbered", Items: []string{"First", "Second", "Third"}}},
			{Kind: "list", Heading: "Bullet list", List: &listBody{Items: []string{"alpha", "beta", "gamma"}}},
			{Kind: "list", Heading: "Checklist", List: &listBody{Marker: "check", Items: []string{"Reproduce", "Fix", "Verify"}}},
			{Kind: "code", Heading: "Code (language hint)", Code: &codeBody{Language: "go", Content: "fmt.Println(\"hello\")"}},
			{Kind: "table", Heading: "Table", Table: &tableBody{Columns: []string{"host", "port"}, Rows: [][]string{{"10.0.0.1", "22"}, {"10.0.0.7", "443"}}}},
			{Kind: "diff", Heading: "Diff", Diff: &diffBody{Before: "old", After: "new"}},
		},
	}
	req, err := json.Marshal(panel)
	if err != nil {
		return writeError(resultPtr, resultCap, "render_demo: "+err.Error())
	}
	reqPtr := stadoAlloc(int32(len(req)))
	defer stadoFree(reqPtr, int32(len(req)))
	copy(wasmBytes(reqPtr, int32(len(req))), req)
	n := stadoUIRender(uint32(reqPtr), uint32(len(req)), uint32(resultPtr), uint32(resultCap))
	if n < 0 {
		return n
	}
	return writeResult(resultPtr, resultCap, "render_demo: panel emitted ("+strconv.Itoa(len(panel.Sections))+" sections)")
}

//go:wasmexport stado_tool_approval_demo
func stadoToolApprovalDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	args := struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: "Plugin approval demo", Body: "Allow the UAT fixture to continue?"}
	if raw := wasmBytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writeError(resultPtr, resultCap, "approval_demo: "+err.Error())
		}
	}
	title := []byte(args.Title)
	body := []byte(args.Body)
	switch stadoUIApprove(bytesPtr(title), uint32(len(title)), bytesPtr(body), uint32(len(body))) {
	case 1:
		return writeResult(resultPtr, resultCap, "approved")
	case 0:
		return writeResult(resultPtr, resultCap, "denied")
	default:
		return writeError(resultPtr, resultCap, "approval UI unavailable")
	}
}

type chooseOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type chooseRequest struct {
	Prompt  string         `json:"prompt"`
	Options []chooseOption `json:"options"`
	Multi   bool           `json:"multi"`
	Default []string       `json:"default,omitempty"`
}

type chooseResponse struct {
	Selected  []string `json:"selected"`
	Cancelled bool     `json:"cancelled"`
}

//go:wasmexport stado_tool_choose_demo
func stadoToolChooseDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	args := chooseRequest{Prompt: "Pick an option", Options: []chooseOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Bravo"}}}
	if raw := wasmBytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writeError(resultPtr, resultCap, "choose_demo: "+err.Error())
		}
	}
	req, err := json.Marshal(args)
	if err != nil {
		return writeError(resultPtr, resultCap, "choose_demo: "+err.Error())
	}
	reqPtr := stadoAlloc(int32(len(req)))
	defer stadoFree(reqPtr, int32(len(req)))
	copy(wasmBytes(reqPtr, int32(len(req))), req)
	n := stadoUIChoose(uint32(reqPtr), uint32(len(req)), uint32(resultPtr), uint32(resultCap))
	if n <= 0 {
		return n
	}
	var resp chooseResponse
	if err := json.Unmarshal(wasmBytes(resultPtr, n), &resp); err != nil {
		return writeError(resultPtr, resultCap, "choose_demo: "+err.Error())
	}
	if resp.Cancelled {
		return writeResult(resultPtr, resultCap, "cancelled")
	}
	return writeResult(resultPtr, resultCap, "selected: "+strings.Join(resp.Selected, ","))
}

func wasmBytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(size))
}

func bytesPtr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

func writeResult(resultPtr, resultCap int32, msg string) int32 {
	if resultCap <= 0 {
		return 0
	}
	dst := wasmBytes(resultPtr, resultCap)
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
