// mcp-client — speak Model Context Protocol over the streamable-HTTP
// transport from inside a stado plugin.
//
// MCP has two wire transports:
//
//   1. stdio — child process over stdin/stdout. Not feasible from a
//      wasm plugin: wazero has no subprocess facility.
//   2. Streamable HTTP — JSON-RPC 2.0 over POST. Server may respond
//      with application/json (single response) or text/event-stream
//      (SSE-framed responses). This plugin implements the HTTP path
//      via stado_http_request.
//
// Tools:
//
//   mcp_init {endpoint, headers?}
//     → {session_id, server_info, capabilities}
//
//   mcp_list_tools {session_id}
//     → {tools: [{name, description, input_schema}]}
//
//   mcp_call_tool {session_id, name, arguments?}
//     → {content: [...], is_error?: bool}
//
// Session caching: each `mcp_init` writes a record to
//   <workdir>/.cache/stado-mcp/<session-id>.json
// containing the endpoint URL, the Mcp-Session-Id header value (if
// the server set one), and any extra headers the caller supplied
// (Authorization, X-API-Key, etc). Subsequent calls look up that
// record and re-send the same headers, so auth + sticky sessions
// survive the wasm-instance freshness model.
//
// Capabilities:
//   - net:http_request
//   - fs:read:.cache/stado-mcp
//   - fs:write:.cache/stado-mcp
//
// Operator-side setup is one mkdir:
//
//   mkdir -p <workdir>/.cache/stado-mcp
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_fs_read
func stadoFsRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFsWrite(pathPtr, pathLen, bufPtr, bufLen uint32) int32

//go:wasmimport stado stado_http_request
func stadoHttpRequest(argsPtr, argsLen, resultPtr, resultCap uint32) int32

func logInfo(msg string) {
	level := []byte("info")
	m := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&level[0]))), uint32(len(level)),
		uint32(uintptr(unsafe.Pointer(&m[0]))), uint32(len(m)),
	)
}

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
func stadoFree(ptr int32, _ int32) {
	pinned.Delete(uintptr(ptr))
}

const (
	cacheDir   = ".cache/stado-mcp"
	httpBufCap = 4 << 20
	mcpVersion = "2025-06-18"
)

type session struct {
	ID            string            `json:"id"`
	Endpoint      string            `json:"endpoint"`
	McpSessionID  string            `json:"mcp_session_id,omitempty"`
	ExtraHeaders  map[string]string `json:"extra_headers,omitempty"`
	ServerInfo    json.RawMessage   `json:"server_info,omitempty"`
	Capabilities  json.RawMessage   `json:"capabilities,omitempty"`
	CreatedUnix   int64             `json:"created_unix"`
}

type errResult struct {
	Error string `json:"error"`
}

type hostHTTPRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
}

type hostHTTPResponse struct {
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	BodyB64       string            `json:"body_b64"`
	BodyTruncated bool              `json:"body_truncated"`
}

// --- mcp_init -----------------------------------------------------------

type initArgs struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type initResult struct {
	SessionID    string          `json:"session_id"`
	ServerInfo   json.RawMessage `json:"server_info,omitempty"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

//go:wasmexport stado_tool_mcp_init
func stadoToolMcpInit(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("mcp_init invoked")

	var a initArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	if strings.TrimSpace(a.Endpoint) == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "endpoint is required"})
	}

	id := newSessionID(a.Endpoint)
	sess := &session{
		ID:           id,
		Endpoint:     a.Endpoint,
		ExtraHeaders: a.Headers,
		CreatedUnix:  time.Now().Unix(),
	}

	// JSON-RPC: initialize.
	initParams := map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "stado-mcp-client",
			"version": "0.1.0",
		},
	}
	resp, mcpSessionID, err := jsonrpc(sess, "initialize", initParams, true)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "initialize: " + err.Error()})
	}
	sess.McpSessionID = mcpSessionID

	// Pull server_info / capabilities out for the caller's convenience.
	var initRes struct {
		ServerInfo   json.RawMessage `json:"serverInfo"`
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &initRes)
	}
	sess.ServerInfo = initRes.ServerInfo
	sess.Capabilities = initRes.Capabilities

	// Spec: client must send notifications/initialized after initialize.
	if _, _, err := jsonrpc(sess, "notifications/initialized", nil, false); err != nil {
		// Non-fatal — some servers ignore the notification. Log and continue.
		logInfo("notifications/initialized failed (non-fatal): " + err.Error())
	}

	if err := saveSession(sess); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "save session: " + err.Error() + " — ensure mkdir -p " + cacheDir + " in workdir",
		})
	}

	return writeJSON(resultPtr, resultCap, initResult{
		SessionID:    sess.ID,
		ServerInfo:   sess.ServerInfo,
		Capabilities: sess.Capabilities,
	})
}

// --- mcp_list_tools -----------------------------------------------------

type listToolsArgs struct {
	SessionID string `json:"session_id"`
}

//go:wasmexport stado_tool_mcp_list_tools
func stadoToolMcpListTools(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("mcp_list_tools invoked")

	var a listToolsArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	sess, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}

	resp, _, err := jsonrpc(sess, "tools/list", nil, true)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "tools/list: " + err.Error()})
	}
	if resp.Error != nil {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: fmt.Sprintf("tools/list rpc error: %d %s", resp.Error.Code, resp.Error.Message),
		})
	}
	// Just relay the result body — the MCP shape ({tools: [...]}) is
	// already what the caller wants.
	return writeRaw(resultPtr, resultCap, resp.Result)
}

// --- mcp_call_tool ------------------------------------------------------

type callToolArgs struct {
	SessionID string          `json:"session_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

//go:wasmexport stado_tool_mcp_call_tool
func stadoToolMcpCallTool(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("mcp_call_tool invoked")

	var a callToolArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	if strings.TrimSpace(a.Name) == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "name is required"})
	}
	sess, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}

	params := map[string]any{
		"name": a.Name,
	}
	if len(a.Arguments) > 0 {
		params["arguments"] = json.RawMessage(a.Arguments)
	}
	resp, _, err := jsonrpc(sess, "tools/call", params, true)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "tools/call: " + err.Error()})
	}
	if resp.Error != nil {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: fmt.Sprintf("tools/call rpc error: %d %s", resp.Error.Code, resp.Error.Message),
		})
	}
	return writeRaw(resultPtr, resultCap, resp.Result)
}

// --- JSON-RPC plumbing --------------------------------------------------

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *jsonrpcResponseEr `json:"error,omitempty"`
}

type jsonrpcResponseEr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var rpcCounter uint32 // atomic

// jsonrpc issues a JSON-RPC call (or a one-way notification when
// expectResponse=false). Returns the parsed response, the value of the
// Mcp-Session-Id response header (if any — only meaningful right
// after `initialize`), and an error.
//
// Streamable-HTTP responses can come back as application/json (one
// frame) or text/event-stream (SSE). We accept both: for SSE we walk
// `data:` lines and pick the JSON-RPC envelope whose id matches.
func jsonrpc(sess *session, method string, params any, expectResponse bool) (*jsonrpcResponse, string, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json, text/event-stream",
	}
	if sess.McpSessionID != "" {
		headers["Mcp-Session-Id"] = sess.McpSessionID
	}
	for k, v := range sess.ExtraHeaders {
		headers[k] = v
	}

	id := int(atomic.AddUint32(&rpcCounter, 1))
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
	}
	if expectResponse {
		req.ID = &id
	}
	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			return nil, "", fmt.Errorf("marshal params: %w", err)
		}
		req.Params = paramsBytes
	}
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	hostReq := hostHTTPRequest{
		Method:    "POST",
		URL:       sess.Endpoint,
		Headers:   headers,
		BodyB64:   base64.StdEncoding.EncodeToString(reqBody),
		TimeoutMs: 30000,
	}
	reqBytes, err := json.Marshal(hostReq)
	if err != nil {
		return nil, "", fmt.Errorf("marshal host request: %w", err)
	}

	scratch := make([]byte, httpBufCap)
	n := stadoHttpRequest(
		uint32(uintptr(unsafe.Pointer(&reqBytes[0]))), uint32(len(reqBytes)),
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(httpBufCap),
	)
	if n < 0 {
		return nil, "", fmt.Errorf("stado_http_request: %s", string(scratch[:-n]))
	}

	var resp hostHTTPResponse
	if err := json.Unmarshal(scratch[:n], &resp); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}

	mcpSessionID := pickHeader(resp.Headers, "Mcp-Session-Id")

	if !expectResponse {
		// Notification — server returns 202 and no body.
		if resp.Status >= 400 {
			return nil, mcpSessionID, fmt.Errorf("HTTP %d", resp.Status)
		}
		return nil, mcpSessionID, nil
	}

	body, err := base64.StdEncoding.DecodeString(resp.BodyB64)
	if err != nil {
		return nil, mcpSessionID, fmt.Errorf("decode body_b64: %w", err)
	}
	if resp.Status >= 400 {
		return nil, mcpSessionID, fmt.Errorf("HTTP %d: %s", resp.Status, truncate(string(body), 256))
	}

	contentType := strings.ToLower(pickHeader(resp.Headers, "Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		rpc, err := pickSSEResponse(body, id)
		return rpc, mcpSessionID, err
	}

	// Default: assume application/json with a single envelope.
	var rpc jsonrpcResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, mcpSessionID, fmt.Errorf("decode jsonrpc: %w (body: %s)", err, truncate(string(body), 256))
	}
	return &rpc, mcpSessionID, nil
}

// pickSSEResponse walks an event-stream body and returns the first
// JSON-RPC envelope whose id matches our request. The transport
// allows the server to send unrelated `notification` frames in the
// same stream — those have no id and we skip them.
func pickSSEResponse(body []byte, wantID int) (*jsonrpcResponse, error) {
	for _, raw := range strings.Split(string(body), "\n\n") {
		var dataLines []string
		for _, line := range strings.Split(raw, "\n") {
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(dataLines) == 0 {
			continue
		}
		joined := strings.Join(dataLines, "\n")
		var rpc jsonrpcResponse
		if err := json.Unmarshal([]byte(joined), &rpc); err != nil {
			continue
		}
		if rpc.ID == nil {
			continue
		}
		var got int
		if err := json.Unmarshal(rpc.ID, &got); err == nil && got == wantID {
			return &rpc, nil
		}
	}
	return nil, fmt.Errorf("no matching JSON-RPC response in SSE stream (%d bytes)", len(body))
}

// --- session storage -----------------------------------------------------

func saveSession(s *session) error {
	path := cacheDir + "/" + s.ID + ".json"
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	pathBytes := []byte(path)
	n := stadoFsWrite(
		uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
		uint32(uintptr(unsafe.Pointer(&body[0]))), uint32(len(body)),
	)
	if n < 0 {
		return fmt.Errorf("stado_fs_write returned -1 — ensure mkdir -p " + cacheDir)
	}
	return nil
}

func loadSession(id string) (*session, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("session_id is required (call mcp_init first)")
	}
	if strings.ContainsAny(id, "/\\.") {
		return nil, fmt.Errorf("invalid session_id")
	}
	path := cacheDir + "/" + id + ".json"
	buf := make([]byte, 1<<16)
	pathBytes := []byte(path)
	n := stadoFsRead(
		uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil, fmt.Errorf("session %s not found (call mcp_init first)", id)
	}
	var s session
	if err := json.Unmarshal(buf[:n], &s); err != nil {
		return nil, fmt.Errorf("corrupted session file: %w", err)
	}
	return &s, nil
}

func newSessionID(endpoint string) string {
	// Stable ID per (endpoint, mtime). Two inits to the same endpoint
	// reuse the same prefix but differ in the timestamp suffix, so
	// repeated init calls don't clobber each other.
	h := sha256.Sum256([]byte(endpoint))
	suffix := time.Now().UnixNano()
	suffixBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(suffixBuf, uint64(suffix))
	return hex.EncodeToString(h[:6]) + "-" + hex.EncodeToString(suffixBuf[4:])
}

// --- helpers -------------------------------------------------------------

func unmarshalArgs(argsPtr, argsLen int32, dst any) error {
	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	if len(args) == 0 {
		return nil
	}
	return json.Unmarshal(args, dst)
}

func pickHeader(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func writeJSON(resultPtr, resultCap int32, v any) int32 {
	payload, err := json.Marshal(v)
	if err != nil {
		return -1
	}
	if int32(len(payload)) > resultCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, payload)
	return int32(len(payload))
}

func writeRaw(resultPtr, resultCap int32, raw json.RawMessage) int32 {
	if len(raw) == 0 {
		return writeJSON(resultPtr, resultCap, struct{}{})
	}
	if int32(len(raw)) > resultCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, raw)
	return int32(len(raw))
}
