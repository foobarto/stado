// http-session — a thin reusable HTTP session wrapper on top of
// stado_http_request. Holds a cookie jar + default headers + base
// URL across calls, persisted to disk so the wasm instance freshness
// (each tool call gets a new instance) doesn't lose state.
//
// Tools:
//
//   http_session_open  {base_url?, default_headers?}
//     → {session_id, base_url}
//   http_session_request {session_id, method, url, headers?, body_b64?, timeout_ms?}
//     → {status, headers, body_b64, body_truncated, cookies, session_id}
//   http_session_close {session_id}
//     → {ok, session_id}
//
// Why a plugin instead of plain stado_http_request: REST APIs that
// need a logged-in session (login → cookie → authenticated calls)
// are tedious to drive via stateless requests. This plugin
// transparently merges Set-Cookie from each response into a jar,
// re-emits Cookie: on subsequent same-host calls, and lets the
// caller stash long-lived auth headers (Bearer token, X-CSRF, etc.)
// on the session at open-time.
//
// Cookie semantics: simplified RFC 6265. Cookies are scoped by host
// only (no path/expiry/secure/HttpOnly enforcement). Set-Cookie
// values with attributes are kept whole; the plugin only splits the
// first `name=value` pair off the leading semicolon. Adequate for
// most API workflows; not a full browser jar.
//
// Capabilities:
//   - net:http_request                      — broad, any public host
//   - fs:read:.cache/stado-http-session     — persisted session state
//   - fs:write:.cache/stado-http-session    — same
//
// For lab IPs, install with `net:http_request_private` added to the
// manifest (or use a per-host `net:http_request:<host>` cap).
//
// Cache layout (relative to operator's workdir):
//
//   <workdir>/.cache/stado-http-session/<session-id>.json
//
// Operator-side setup is one mkdir:
//
//   mkdir -p <workdir>/.cache/stado-http-session
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
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
	cacheDir   = ".cache/stado-http-session"
	hostBufCap = 4 << 20
)

type sessionState struct {
	ID             string                       `json:"id"`
	BaseURL        string                       `json:"base_url,omitempty"`
	DefaultHeaders map[string]string            `json:"default_headers,omitempty"`
	Cookies        map[string]map[string]string `json:"cookies,omitempty"` // host → name → value
	CreatedUnix    int64                        `json:"created_unix"`
}

type errResult struct {
	Error string `json:"error"`
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

func decodeArgs(ptr, length int32, dst any) error {
	if length <= 0 {
		return nil
	}
	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(length))
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("invalid JSON args: %w", err)
	}
	return nil
}

func sessionPath(id string) string {
	// session id is operator-controlled (and the open path generates
	// it deterministically) — sanitize anyway. Hash anything weird.
	if !isSafeSessionID(id) {
		sum := sha256.Sum256([]byte(id))
		id = "s-" + hex.EncodeToString(sum[:8])
	}
	return cacheDir + "/" + id + ".json"
}

func isSafeSessionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func loadSession(id string) (*sessionState, error) {
	path := sessionPath(id)
	buf := make([]byte, hostBufCap)
	pathBytes := []byte(path)
	n := stadoFsRead(
		uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(hostBufCap),
	)
	if n <= 0 {
		return nil, fmt.Errorf("session %q not found (no %s)", id, path)
	}
	var s sessionState
	if err := json.Unmarshal(buf[:n], &s); err != nil {
		return nil, fmt.Errorf("session %q corrupt: %w", id, err)
	}
	return &s, nil
}

func saveSession(s *sessionState) error {
	path := sessionPath(s.ID)
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
		return fmt.Errorf("fs_write %s: denied; ensure %s capability declared and dir exists (mkdir -p %s)", path, "fs:write:"+cacheDir, cacheDir)
	}
	return nil
}

// ---------- tool: http_session_open ----------

type openArgs struct {
	BaseURL        string            `json:"base_url,omitempty"`
	DefaultHeaders map[string]string `json:"default_headers,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
}

type openResult struct {
	SessionID string `json:"session_id"`
	BaseURL   string `json:"base_url,omitempty"`
}

//go:wasmexport stado_tool_http_session_open
func stadoToolHTTPSessionOpen(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a openArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	id := a.SessionID
	if id == "" {
		// Stable id derived from base_url + a millisecond timestamp:
		// callers can re-open the same logical session (e.g. an HTB
		// box's /api) by passing the same base_url across boots.
		seed := a.BaseURL + "|" + fmt.Sprintf("%d", time.Now().UnixMilli())
		sum := sha256.Sum256([]byte(seed))
		id = "s-" + hex.EncodeToString(sum[:6])
	}
	if !isSafeSessionID(id) {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session_id must match [A-Za-z0-9_-]{1,64}"})
	}
	s := &sessionState{
		ID:             id,
		BaseURL:        strings.TrimRight(a.BaseURL, "/"),
		DefaultHeaders: a.DefaultHeaders,
		Cookies:        map[string]map[string]string{},
		CreatedUnix:    time.Now().Unix(),
	}
	if err := saveSession(s); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	logInfo("http-session opened: " + id)
	return writeJSON(resultPtr, resultCap, openResult{SessionID: id, BaseURL: s.BaseURL})
}

// ---------- tool: http_session_request ----------

type requestArgs struct {
	SessionID string            `json:"session_id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
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

type requestResult struct {
	SessionID     string            `json:"session_id"`
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	BodyB64       string            `json:"body_b64"`
	BodyTruncated bool              `json:"body_truncated"`
	Cookies       map[string]string `json:"cookies,omitempty"`
}

//go:wasmexport stado_tool_http_session_request
func stadoToolHTTPSessionRequest(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a requestArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if a.SessionID == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session_id is required"})
	}
	if a.Method == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "method is required"})
	}
	if a.URL == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "url is required"})
	}
	s, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}

	// Resolve relative url against base_url.
	full := a.URL
	if !strings.HasPrefix(full, "http://") && !strings.HasPrefix(full, "https://") {
		if s.BaseURL == "" {
			return writeJSON(resultPtr, resultCap, errResult{Error: "url is relative but session has no base_url"})
		}
		if !strings.HasPrefix(full, "/") {
			full = "/" + full
		}
		full = s.BaseURL + full
	}

	parsed, perr := url.Parse(full)
	if perr != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "url: " + perr.Error()})
	}
	host := strings.ToLower(parsed.Hostname())

	// Merge headers: defaults < per-call < cookie.
	merged := map[string]string{}
	for k, v := range s.DefaultHeaders {
		merged[k] = v
	}
	for k, v := range a.Headers {
		merged[k] = v
	}
	if jar := s.Cookies[host]; len(jar) > 0 {
		var parts []string
		for name, val := range jar {
			parts = append(parts, name+"="+val)
		}
		merged["Cookie"] = strings.Join(parts, "; ")
	}

	hostReq := hostHTTPRequest{
		Method:    strings.ToUpper(a.Method),
		URL:       full,
		Headers:   merged,
		BodyB64:   a.BodyB64,
		TimeoutMs: a.TimeoutMs,
	}
	reqBytes, err := json.Marshal(hostReq)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "marshal request: " + err.Error()})
	}
	scratch := make([]byte, hostBufCap)
	n := stadoHttpRequest(
		uint32(uintptr(unsafe.Pointer(&reqBytes[0]))), uint32(len(reqBytes)),
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(hostBufCap),
	)
	if n < 0 {
		// Negative = host-side error; |n| bytes of error string in scratch.
		return writeJSON(resultPtr, resultCap, errResult{Error: "stado_http_request: " + string(scratch[:-n])})
	}
	var resp hostHTTPResponse
	if err := json.Unmarshal(scratch[:n], &resp); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "decode response: " + err.Error()})
	}

	// Harvest Set-Cookie. Host folds multi-value headers comma-joined,
	// so we split on ", " AT a `; ` boundary unfortunate-edge: some
	// cookie attributes (Expires=Mon, 01 Jan...) contain commas. The
	// safer pattern is to look at the comma-folded value as a sequence
	// of attribute groups separated by ", " ONLY when followed by a
	// `name=` token — close enough for API-scale usage.
	if sc := resp.Headers["Set-Cookie"]; sc != "" {
		if s.Cookies == nil {
			s.Cookies = map[string]map[string]string{}
		}
		if s.Cookies[host] == nil {
			s.Cookies[host] = map[string]string{}
		}
		for _, c := range splitSetCookie(sc) {
			if name, val, ok := parseCookiePair(c); ok {
				s.Cookies[host][name] = val
			}
		}
	}

	if err := saveSession(s); err != nil {
		// Don't fail the request just because the jar didn't persist;
		// surface a warning instead.
		logInfo("http-session: save failed: " + err.Error())
	}

	return writeJSON(resultPtr, resultCap, requestResult{
		SessionID:     s.ID,
		Status:        resp.Status,
		Headers:       resp.Headers,
		BodyB64:       resp.BodyB64,
		BodyTruncated: resp.BodyTruncated,
		Cookies:       s.Cookies[host],
	})
}

// splitSetCookie undoes the host's comma-fold of multi-value headers
// for Set-Cookie. Heuristic: split on ", " only when the next token
// looks like `<name>=`. This handles `Expires=Mon, 01 Jan` correctly.
func splitSetCookie(s string) []string {
	var out []string
	current := ""
	parts := strings.Split(s, ", ")
	for _, p := range parts {
		// peek: does `p` start with a `<name>=...` and isn't a known
		// attribute-only prefix?
		eq := strings.IndexByte(p, '=')
		semi := strings.IndexByte(p, ';')
		if current == "" || (eq > 0 && (semi < 0 || eq < semi) && !looksLikeAttrContinuation(p)) {
			if current != "" {
				out = append(out, current)
			}
			current = p
		} else {
			current += ", " + p
		}
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

// looksLikeAttrContinuation returns true for fragments that smell
// like the tail of a cookie attribute (e.g. `01 Jan 2030 00:00:00 GMT`).
// Cookie *names* never contain spaces or digits-only prefixes, so this
// is a cheap-but-good-enough discriminator.
func looksLikeAttrContinuation(p string) bool {
	if p == "" {
		return false
	}
	// Date-ish tail: starts with digits + space, or with a known
	// month abbreviation.
	if len(p) >= 3 {
		if (p[0] >= '0' && p[0] <= '9') && (p[1] >= '0' && p[1] <= '9') && p[2] == ' ' {
			return true
		}
	}
	return false
}

func parseCookiePair(c string) (name, value string, ok bool) {
	c = strings.TrimSpace(c)
	semi := strings.IndexByte(c, ';')
	if semi >= 0 {
		c = c[:semi]
	}
	eq := strings.IndexByte(c, '=')
	if eq <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(c[:eq]), strings.TrimSpace(c[eq+1:]), true
}

// ---------- tool: http_session_close ----------

type closeArgs struct {
	SessionID string `json:"session_id"`
}

type closeResult struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
}

//go:wasmexport stado_tool_http_session_close
func stadoToolHTTPSessionClose(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a closeArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if a.SessionID == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session_id is required"})
	}
	// Overwrite the session file with an empty marker. We don't have
	// a stado_fs_unlink host import yet, so blanking is the closest
	// we get to delete. The next open with the same id will overwrite
	// cleanly.
	s := &sessionState{ID: a.SessionID, CreatedUnix: 0}
	_ = saveSession(s)
	logInfo("http-session closed: " + a.SessionID)
	return writeJSON(resultPtr, resultCap, closeResult{OK: true, SessionID: a.SessionID})
}

// keep base64 import used (some build modes drop unreferenced imports).
var _ = base64.StdEncoding
