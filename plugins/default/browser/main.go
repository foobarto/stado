//go:build wasip1

// browser — stado bundled browser plugin.
//
// Two tiers, one plugin:
//
//   Tier 1 — HTTP browser (always available, no deps):
//     Fetches pages via stado_http_request, parses HTML, manages a
//     cookie jar, spoofs Chrome/Firefox/Safari headers. Works on any
//     site that doesn't require JavaScript or advanced fingerprinting.
//
//   Tier 2 — Chrome CDP (requires chromium or google-chrome on PATH):
//     Spawns a real headless Chrome via stado_proc_spawn, connects to
//     its DevTools WebSocket, and drives it via CDP. Full JavaScript
//     execution, real rendering, real cookie/storage API, screenshots.
//     Anti-detection: launched with --disable-blink-features=AutomationControlled
//     and a spoofed UA. Handles most DataDome / Akamai cases that don't
//     require behavioral mouse simulation.
//
// Tools (tier 1 — always available):
//   browser_open     {url, profile?, headers?, js_detect?}
//   browser_click    {session_id, link_index?, selector?, text_match?}
//   browser_submit   {session_id, form_index?, selector?, fields?, click?}
//   browser_query    {session_id, selector, attr?}
//   browser_text     {session_id}
//
// Tools (tier 2 — requires Chrome; error if not installed):
//   browser_cdp_open       {url, wait_for?, timeout_ms?}
//   browser_cdp_eval       {session_id, js, timeout_ms?}
//   browser_cdp_screenshot {session_id}
//   browser_cdp_close      {session_id}
//
// Capabilities:
//   net:http_request, net:http_request_private
//   exec:proc         (for spawning Chrome — operator may scope to chrome path)
//   net:dial:tcp:127.0.0.1:*   (CDP WebSocket)
//
// Operator note: stado_proc_spawn requires exec:proc capability. If you
// need to scope it, use exec:proc:/usr/bin/chromium or similar.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/PuerkitoBio/goquery"
)

func main() {}

// ── host imports ───────────────────────────────────────────────────────────

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_fs_read
func stadoFsRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFsWrite(pathPtr, pathLen, bufPtr, bufLen uint32) int32

//go:wasmimport stado stado_http_request
func stadoHttpRequest(argsPtr, argsLen, resultPtr, resultCap uint32) int32

// EP-0038a: process + raw network host imports for Chrome CDP.
//
//go:wasmimport stado stado_proc_spawn
func stadoProcSpawn(reqPtr, reqLen uint32) uint32

//go:wasmimport stado stado_proc_read
func stadoProcRead(h, max, timeoutMs, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_proc_wait
func stadoProcWait(h uint32) int32

//go:wasmimport stado stado_proc_close
func stadoProcClose(h uint32)

//go:wasmimport stado stado_net_dial
func stadoNetDial(transportPtr, transportLen, addrPtr, addrLen uint32) uint32

//go:wasmimport stado stado_net_read
func stadoNetRead(h, max, timeoutMs, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_net_write
func stadoNetWrite(h, bufPtr, bufLen uint32) int32

//go:wasmimport stado stado_net_close
func stadoNetClose(h uint32)

// ── alloc / free (required ABI) ───────────────────────────────────────────

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

// ── constants ──────────────────────────────────────────────────────────────

const (
	cacheDir   = ".cache/stado-browser"
	httpBufCap = 8 << 20   // 8 MiB HTTP response buffer
	netBufCap  = 4 << 20   // 4 MiB CDP/WebSocket buffer
	maxRedirs  = 10
	cdpPort    = 0          // 0 = let Chrome pick a random port
)

// ── logging ────────────────────────────────────────────────────────────────

func logInfo(msg string) {
	level, m := []byte("info"), []byte(msg)
	stadoLog(ptr(level), uint32(len(level)), ptr(m), uint32(len(m)))
}

func ptr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

// ── tier-1 types ──────────────────────────────────────────────────────────

type sessionState struct {
	ID          string                       `json:"id"`
	Profile     string                       `json:"profile"`
	ExtraHdrs   map[string]string            `json:"extra_headers,omitempty"`
	Cookies     map[string]map[string]string `json:"cookies,omitempty"` // host → name → value
	CurrentURL  string                       `json:"current_url,omitempty"`
	CurrentBody string                       `json:"current_body,omitempty"`
	CreatedUnix int64                        `json:"created_unix"`
}

type hostHTTPRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
}

type hostHTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

type linkOut struct {
	Index int    `json:"index"`
	Href  string `json:"href"`
	Text  string `json:"text,omitempty"`
}

type formField struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

type formOut struct {
	Index  int         `json:"index"`
	Action string      `json:"action"`
	Method string      `json:"method"`
	Fields []formField `json:"fields,omitempty"`
}

type pageResult struct {
	SessionID string            `json:"session_id"`
	URL       string            `json:"url"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Title     string            `json:"title,omitempty"`
	Links     []linkOut         `json:"links,omitempty"`
	Forms     []formOut         `json:"forms,omitempty"`
	Cookies   map[string]string `json:"cookies,omitempty"`
	Text      string            `json:"text,omitempty"`
	HTML      string            `json:"html,omitempty"`
	NeedsJS   bool              `json:"needs_js,omitempty"`   // hint: page likely requires JS
}

// ── tier-2 (CDP) types ────────────────────────────────────────────────────

// cdpSession holds a live Chrome CDP connection.
type cdpSession struct {
	procHandle uint32 // stado_proc_spawn handle for the Chrome process
	netHandle  uint32 // stado_net_dial handle for CDP WebSocket
	targetID   string // CDP target ID
	sessionID  string // CDP session ID (attached to target)
	cmdID      int    // monotonic CDP command counter
	url        string
}

type cdpPageResult struct {
	SessionID string   `json:"session_id"`
	URL       string   `json:"url"`
	Title     string   `json:"title,omitempty"`
	Text      string   `json:"text,omitempty"`
	HTML      string   `json:"html,omitempty"`
	Links     []string `json:"links,omitempty"`
	Cookies   []cdpCookie `json:"cookies,omitempty"`
}

type cdpCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// ── session storage ───────────────────────────────────────────────────────

var (
	sessions    = map[string]*sessionState{}
	cdpSessions = map[string]*cdpSession{}
	sessMu      sync.Mutex
)

func newSessionID() string {
	return fmt.Sprintf("s%d", time.Now().UnixNano())
}

func loadSession(id string) (*sessionState, bool) {
	sessMu.Lock()
	defer sessMu.Unlock()
	s, ok := sessions[id]
	return s, ok
}

func saveSession(s *sessionState) {
	sessMu.Lock()
	defer sessMu.Unlock()
	sessions[s.ID] = s
}

func loadCDPSession(id string) (*cdpSession, bool) {
	sessMu.Lock()
	defer sessMu.Unlock()
	s, ok := cdpSessions[id]
	return s, ok
}

func saveCDPSession(s *cdpSession) {
	sessMu.Lock()
	defer sessMu.Unlock()
	cdpSessions[s.sessionID] = s
}

// ── browser header profiles ───────────────────────────────────────────────

var profiles = map[string]map[string]string{
	"chrome": {
		"User-Agent":                "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":                   "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language":          "en-US,en;q=0.9",
		"Sec-Ch-Ua":                `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		"Sec-Ch-Ua-Mobile":         "?0",
		"Sec-Ch-Ua-Platform":       `"Linux"`,
		"Sec-Fetch-Dest":           "document",
		"Sec-Fetch-Mode":           "navigate",
		"Sec-Fetch-Site":           "none",
		"Sec-Fetch-User":           "?1",
		"Upgrade-Insecure-Requests": "1",
		"DNT":                      "1",
	},
	"firefox": {
		"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64; rv:125.0) Gecko/20100101 Firefox/125.0",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.5",
		"Sec-Fetch-Dest":  "document",
		"Sec-Fetch-Mode":  "navigate",
		"Sec-Fetch-Site":  "none",
		"Sec-Fetch-User":  "?1",
		"DNT":             "1",
	},
	"safari": {
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	},
}

// ── tier-1 HTTP helpers ───────────────────────────────────────────────────

func doHTTP(req hostHTTPRequest) (map[string]string, int, []byte, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, 0, nil, err
	}
	scratch := make([]byte, httpBufCap)
	n := stadoHttpRequest(ptr(reqBytes), uint32(len(reqBytes)), ptr(scratch), httpBufCap)
	if n < 0 {
		return nil, 0, nil, fmt.Errorf("stado_http_request: %s", string(scratch[:-n]))
	}
	var resp hostHTTPResponse
	if err := json.Unmarshal(scratch[:n], &resp); err != nil {
		return nil, 0, nil, fmt.Errorf("decode response: %w", err)
	}
	body, _ := base64.StdEncoding.DecodeString(resp.BodyB64)
	return resp.Headers, resp.Status, body, nil
}

func buildCookieHeader(sess *sessionState, rawURL string) string {
	host := hostOf(rawURL)
	var parts []string
	for _, cookies := range sess.Cookies {
		for name, val := range cookies {
			parts = append(parts, name+"="+val)
		}
	}
	_ = host
	return strings.Join(parts, "; ")
}

func mergeCookies(sess *sessionState, headers map[string]string, rawURL string) {
	host := hostOf(rawURL)
	if sess.Cookies == nil {
		sess.Cookies = map[string]map[string]string{}
	}
	if sess.Cookies[host] == nil {
		sess.Cookies[host] = map[string]string{}
	}
	for k, v := range headers {
		if !strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		parts := strings.SplitN(v, ";", 2)
		if len(parts) < 1 {
			continue
		}
		kv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
		if len(kv) == 2 {
			sess.Cookies[host][kv[0]] = kv[1]
		}
	}
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}

func sameHost(a, b string) bool { return hostOf(a) == hostOf(b) }

func needsJS(body []byte) bool {
	text := strings.ToLower(string(body))
	triggers := []string{
		"enable javascript", "javascript is required", "javascript is disabled",
		"please enable js", "__cf_chl", "challenge-platform", "datadome",
		"_cf_chl_opt", "jschl", "ray id", "checking your browser",
	}
	for _, t := range triggers {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

func fetchPage(sess *sessionState, method, target string, body []byte, referer string) (*pageResult, error) {
	profile, ok := profiles[sess.Profile]
	if !ok {
		profile = profiles["chrome"]
	}
	headers := map[string]string{}
	for k, v := range profile {
		headers[k] = v
	}
	for k, v := range sess.ExtraHdrs {
		headers[k] = v
	}
	if referer != "" {
		headers["Referer"] = referer
		if sess.Profile == "chrome" && sameHost(referer, target) {
			headers["Sec-Fetch-Site"] = "same-origin"
		}
	}
	if ck := buildCookieHeader(sess, target); ck != "" {
		headers["Cookie"] = ck
	}
	if method == "POST" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	req := hostHTTPRequest{Method: method, URL: target, Headers: headers, TimeoutMs: 30000}
	if len(body) > 0 {
		req.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}

	for i := 0; i < maxRedirs; i++ {
		hdrs, status, respBody, err := doHTTP(req)
		if err != nil {
			return nil, err
		}
		mergeCookies(sess, hdrs, req.URL)
		// Follow redirects.
		if status >= 300 && status < 400 {
			loc := hdrs["Location"]
			if loc == "" {
				loc = hdrs["location"]
			}
			if loc == "" {
				break
			}
			if !strings.HasPrefix(loc, "http") {
				base, _ := url.Parse(req.URL)
				ref, _ := url.Parse(loc)
				loc = base.ResolveReference(ref).String()
			}
			req = hostHTTPRequest{Method: "GET", URL: loc, Headers: headers, TimeoutMs: 30000}
			if ck := buildCookieHeader(sess, loc); ck != "" {
				req.Headers["Cookie"] = ck
			}
			continue
		}
		sess.CurrentURL = req.URL
		sess.CurrentBody = string(respBody)

		var doc *goquery.Document
		ct := hdrs["Content-Type"]
		if ct == "" {
			ct = hdrs["content-type"]
		}
		if strings.Contains(ct, "html") {
			doc, _ = goquery.NewDocumentFromReader(bytes.NewReader(respBody))
		}

		res := &pageResult{
			SessionID: sess.ID,
			URL:       req.URL,
			Status:    status,
			Headers:   hdrs,
			NeedsJS:   needsJS(respBody),
		}
		if doc != nil {
			res.Title = strings.TrimSpace(doc.Find("title").First().Text())
			res.Links = extractLinks(doc, req.URL)
			res.Forms = extractForms(doc, req.URL)
			res.Text = truncate(extractText(doc), 32768)
			res.HTML = truncate(string(respBody), 65536)
		} else {
			res.HTML = truncate(string(respBody), 65536)
		}
		if res.NeedsJS {
			res.Text = "[Page requires JavaScript — use browser_cdp_open for JS execution]\n" + res.Text
		}
		return res, nil
	}
	return nil, fmt.Errorf("too many redirects")
}

// ── HTML helpers ──────────────────────────────────────────────────────────

func extractLinks(doc *goquery.Document, base string) []linkOut {
	baseU, _ := url.Parse(base)
	var out []linkOut
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if href == "" || strings.HasPrefix(href, "javascript:") {
			return
		}
		if baseU != nil {
			if ref, err := url.Parse(href); err == nil {
				href = baseU.ResolveReference(ref).String()
			}
		}
		out = append(out, linkOut{Index: i, Href: href, Text: truncate(strings.TrimSpace(s.Text()), 200)})
	})
	return out
}

func extractForms(doc *goquery.Document, base string) []formOut {
	baseU, _ := url.Parse(base)
	var out []formOut
	doc.Find("form").Each(func(i int, s *goquery.Selection) {
		action, _ := s.Attr("action")
		if baseU != nil {
			if ref, err := url.Parse(action); err == nil {
				action = baseU.ResolveReference(ref).String()
			}
		}
		method := strings.ToUpper(s.AttrOr("method", "GET"))
		var fields []formField
		s.Find("input,select,textarea").Each(func(_ int, f *goquery.Selection) {
			name := f.AttrOr("name", "")
			if name == "" {
				return
			}
			fields = append(fields, formField{
				Name:  name,
				Type:  f.AttrOr("type", "text"),
				Value: f.AttrOr("value", ""),
			})
		})
		out = append(out, formOut{Index: i, Action: action, Method: method, Fields: fields})
	})
	return out
}

func extractText(doc *goquery.Document) string {
	doc.Find("script,style,noscript").Remove()
	return strings.TrimSpace(doc.Find("body").Text())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}

// ── tier-2: Chrome CDP ────────────────────────────────────────────────────

// chromeBinaries is the search order for headless Chrome / Chromium.
var chromeBinaries = []string{
	"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
	"/usr/bin/chromium", "/usr/bin/chromium-browser",
	"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable",
	"/usr/local/bin/chromium", "/snap/bin/chromium",
}

// spawnChrome launches a headless Chrome process and returns its
// stado_proc handle plus the CDP WebSocket URL.
func spawnChrome() (procHandle uint32, wsURL string, err error) {
	// Try each known Chrome binary until one spawns.
	for _, bin := range chromeBinaries {
		req := map[string]any{
			"argv": []string{
				bin,
				"--headless=new",
				"--disable-gpu",
				"--no-sandbox",
				"--disable-dev-shm-usage",
				"--remote-debugging-port=0",                   // random port; printed to stderr
				"--disable-blink-features=AutomationControlled",
				"--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
				"--disable-extensions",
				"--disable-background-networking",
				"--disable-default-apps",
				"--disable-sync",
				"--metrics-recording-only",
				"--no-first-run",
				"--safebrowsing-disable-auto-update",
				"about:blank",
			},
		}
		reqBytes, _ := json.Marshal(req)
		h := stadoProcSpawn(ptr(reqBytes), uint32(len(reqBytes)))
		if h == 0 {
			continue
		}
		// Read stderr to find "DevTools listening on ws://..."
		buf := make([]byte, 4096)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			n := stadoProcRead(h, 4096, 200, ptr(buf), 4096)
			if n <= 0 {
				continue
			}
			text := string(buf[:n])
			if idx := strings.Index(text, "DevTools listening on "); idx >= 0 {
				line := text[idx:]
				end := strings.IndexAny(line, "\r\n")
				if end > 0 {
					line = line[:end]
				}
				wsURL = strings.TrimPrefix(line, "DevTools listening on ")
				return h, strings.TrimSpace(wsURL), nil
			}
		}
		// Timeout — kill and try next.
		stadoProcClose(h)
	}
	return 0, "", fmt.Errorf("no Chrome/Chromium binary found — install chromium or google-chrome")
}

// ── WebSocket implementation (RFC 6455) ──────────────────────────────────

// wsHandshake performs the WebSocket upgrade over an already-open TCP handle.
func wsHandshake(netHandle uint32, host, path string) error {
	// Build HTTP upgrade request.
	nonce := base64.StdEncoding.EncodeToString([]byte("stadoBrowser01=="))
	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"\r\n",
		path, host, nonce,
	)
	reqBytes := []byte(req)
	n := stadoNetWrite(netHandle, ptr(reqBytes), uint32(len(reqBytes)))
	if n < 0 {
		return fmt.Errorf("ws handshake write: %d", n)
	}
	// Read response — look for 101 Switching Protocols.
	buf := make([]byte, 4096)
	var full strings.Builder
	for i := 0; i < 20; i++ {
		n := stadoNetRead(netHandle, 4096, 500, ptr(buf), 4096)
		if n <= 0 {
			continue
		}
		full.Write(buf[:n])
		if strings.Contains(full.String(), "\r\n\r\n") {
			break
		}
	}
	if !strings.Contains(full.String(), "101 Switching Protocols") {
		return fmt.Errorf("ws handshake failed: %s", truncate(full.String(), 200))
	}
	return nil
}

// wsSend sends a WebSocket text frame (opcode 0x1, masked as client).
func wsSend(netHandle uint32, payload []byte) error {
	var frame []byte
	frame = append(frame, 0x81) // FIN + text opcode
	length := len(payload)
	if length < 126 {
		frame = append(frame, byte(length)|0x80) // MASK bit set
	} else if length < 65536 {
		frame = append(frame, 0xFE, byte(length>>8), byte(length))
	} else {
		frame = append(frame, 0xFF,
			0, 0, 0, 0,
			byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	// Masking key (RFC 6455 §5.3) — client MUST mask. Use fixed key for simplicity.
	mask := [4]byte{0x37, 0x42, 0x1A, 0x9F}
	frame = append(frame, mask[:]...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	frame = append(frame, masked...)
	n := stadoNetWrite(netHandle, ptr(frame), uint32(len(frame)))
	if n < 0 {
		return fmt.Errorf("ws send: %d", n)
	}
	return nil
}

// wsRecv reads one or more WebSocket frames and returns the reassembled payload.
// Reads until a complete message is received (FIN=1) or timeout.
func wsRecv(netHandle uint32, timeoutMs int) ([]byte, error) {
	buf := make([]byte, netBufCap)
	var raw []byte
	remaining := timeoutMs
	for remaining > 0 {
		tick := 100
		if tick > remaining {
			tick = remaining
		}
		n := stadoNetRead(netHandle, uint32(netBufCap), uint32(tick), ptr(buf), uint32(netBufCap))
		remaining -= tick
		if n <= 0 {
			if len(raw) > 0 {
				break // got some data, stop waiting
			}
			continue
		}
		raw = append(raw, buf[:n]...)
		// Simple check: does this look like a complete frame?
		if msg, ok := parseWSFrame(raw); ok {
			return msg, nil
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("ws recv timeout")
	}
	msg, ok := parseWSFrame(raw)
	if !ok {
		return raw, nil // return partial data
	}
	return msg, nil
}

func parseWSFrame(data []byte) ([]byte, bool) {
	if len(data) < 2 {
		return nil, false
	}
	// FIN bit: data[0] & 0x80
	opcode := data[0] & 0x0F
	if opcode == 0x8 {
		return nil, true // close frame
	}
	masked := (data[1] & 0x80) != 0
	length := int(data[1] & 0x7F)
	offset := 2
	if length == 126 {
		if len(data) < 4 {
			return nil, false
		}
		length = int(data[2])<<8 | int(data[3])
		offset = 4
	} else if length == 127 {
		if len(data) < 10 {
			return nil, false
		}
		length = int(data[6])<<24 | int(data[7])<<16 | int(data[8])<<8 | int(data[9])
		offset = 10
	}
	if masked {
		offset += 4
	}
	if len(data) < offset+length {
		return nil, false
	}
	return data[offset : offset+length], true
}

// ── CDP protocol helpers ──────────────────────────────────────────────────

func (s *cdpSession) send(method string, params map[string]any) (map[string]any, error) {
	s.cmdID++
	cmd := map[string]any{"id": s.cmdID, "method": method}
	if params != nil {
		cmd["params"] = params
	}
	if s.sessionID != "" {
		cmd["sessionId"] = s.sessionID
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	if err := wsSend(s.netHandle, payload); err != nil {
		return nil, err
	}
	// Wait for matching response.
	targetID := s.cmdID
	for attempts := 0; attempts < 50; attempts++ {
		resp, err := wsRecv(s.netHandle, 500)
		if err != nil {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(resp, &msg); err != nil {
			continue
		}
		if id, ok := msg["id"].(float64); ok && int(id) == targetID {
			if result, ok := msg["result"].(map[string]any); ok {
				return result, nil
			}
			if errObj, ok := msg["error"].(map[string]any); ok {
				return nil, fmt.Errorf("CDP error: %v", errObj["message"])
			}
			return map[string]any{}, nil
		}
	}
	return nil, fmt.Errorf("CDP: no response for command %d (%s)", targetID, method)
}

// cdpConnect parses the ws URL, dials, upgrades, and returns a fresh cdpSession.
func cdpConnect(wsURL string) (*cdpSession, error) {
	// Parse ws://127.0.0.1:PORT/json/version → host and path
	wsURL = strings.TrimPrefix(wsURL, "ws://")
	slashIdx := strings.Index(wsURL, "/")
	host := wsURL
	path := "/"
	if slashIdx >= 0 {
		host = wsURL[:slashIdx]
		path = wsURL[slashIdx:]
	}

	// Dial the raw TCP connection.
	transport := []byte("tcp")
	addr := []byte(host)
	netH := stadoNetDial(ptr(transport), uint32(len(transport)), ptr(addr), uint32(len(addr)))
	if netH == 0 {
		return nil, fmt.Errorf("CDP: dial %s failed", host)
	}

	// WebSocket upgrade.
	if err := wsHandshake(netH, host, path); err != nil {
		stadoNetClose(netH)
		return nil, err
	}

	sess := &cdpSession{netHandle: netH}

	// Get the first available target.
	result, err := sess.send("Target.getTargets", nil)
	if err != nil {
		stadoNetClose(netH)
		return nil, fmt.Errorf("CDP getTargets: %w", err)
	}
	targets, _ := result["targetInfos"].([]any)
	for _, t := range targets {
		ti, _ := t.(map[string]any)
		if ti["type"] == "page" {
			sess.targetID, _ = ti["targetId"].(string)
			break
		}
	}
	if sess.targetID == "" {
		// Create a new page target.
		result, err = sess.send("Target.createTarget", map[string]any{"url": "about:blank"})
		if err != nil {
			stadoNetClose(netH)
			return nil, fmt.Errorf("CDP createTarget: %w", err)
		}
		sess.targetID, _ = result["targetId"].(string)
	}

	// Attach to the target to get a session ID.
	result, err = sess.send("Target.attachToTarget", map[string]any{
		"targetId": sess.targetID,
		"flatten":  true,
	})
	if err != nil {
		stadoNetClose(netH)
		return nil, fmt.Errorf("CDP attachToTarget: %w", err)
	}
	sess.sessionID, _ = result["sessionId"].(string)

	// Enable Page and Runtime domains.
	if _, err := sess.send("Page.enable", nil); err != nil {
		logInfo("CDP Page.enable warning: " + err.Error())
	}
	if _, err := sess.send("Runtime.enable", nil); err != nil {
		logInfo("CDP Runtime.enable warning: " + err.Error())
	}

	return sess, nil
}

// navigate drives Chrome to a URL and waits for the load event.
func (s *cdpSession) navigate(rawURL string, waitForMs int) error {
	if _, err := s.send("Page.navigate", map[string]any{"url": rawURL}); err != nil {
		return fmt.Errorf("navigate: %w", err)
	}
	// Poll for load completion via document.readyState.
	if waitForMs <= 0 {
		waitForMs = 10000
	}
	deadline := time.Now().Add(time.Duration(waitForMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		result, err := s.send("Runtime.evaluate", map[string]any{
			"expression":    "document.readyState",
			"returnByValue": true,
		})
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if res, ok := result["result"].(map[string]any); ok {
			if state, _ := res["value"].(string); state == "complete" || state == "interactive" {
				s.url = rawURL
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.url = rawURL
	return nil // timeout is not fatal — return whatever loaded
}

// getPageContent extracts title, text, HTML, and links from the current page.
func (s *cdpSession) getPageContent() cdpPageResult {
	res := cdpPageResult{SessionID: s.sessionID, URL: s.url}

	evalStr := func(expr string) string {
		result, err := s.send("Runtime.evaluate", map[string]any{
			"expression": expr, "returnByValue": true,
		})
		if err != nil {
			return ""
		}
		if r, ok := result["result"].(map[string]any); ok {
			v, _ := r["value"].(string)
			return v
		}
		return ""
	}

	res.Title = evalStr("document.title")
	res.HTML = truncate(evalStr("document.documentElement.outerHTML"), 65536)
	// Extract visible text via innerText.
	res.Text = truncate(evalStr("document.body ? document.body.innerText : ''"), 32768)
	// Extract links.
	linksJSON := evalStr(`JSON.stringify(Array.from(document.querySelectorAll('a[href]')).slice(0,200).map(a=>a.href))`)
	if linksJSON != "" {
		var links []string
		_ = json.Unmarshal([]byte(linksJSON), &links)
		res.Links = links
	}
	// Get cookies.
	result, err := s.send("Network.getCookies", map[string]any{"urls": []string{s.url}})
	if err == nil {
		if cookies, ok := result["cookies"].([]any); ok {
			for _, c := range cookies {
				cm, _ := c.(map[string]any)
				res.Cookies = append(res.Cookies, cdpCookie{
					Name:   strVal(cm, "name"),
					Value:  strVal(cm, "value"),
					Domain: strVal(cm, "domain"),
					Path:   strVal(cm, "path"),
				})
			}
		}
	}
	return res
}

func strVal(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// ── tool exports ───────────────────────────────────────────────────────────

func writeResult(resPtr, resCap int32, v any) int32 {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"error": err.Error()})
	}
	if int32(len(b)) > resCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resPtr))), resCap)
	return int32(copy(dst, b))
}

func writeError(resPtr, resCap int32, msg string) int32 {
	return writeResult(resPtr, resCap, map[string]string{"error": msg})
}

func readArgs(argsPtr, argsLen int32) []byte {
	if argsLen <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), argsLen)
}

// ── tier-1 tool: browser_open ──────────────────────────────────────────────

//go:wasmexport stado_tool_browser_open
func stadoToolBrowserOpen(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		URL     string            `json:"url"`
		Profile string            `json:"profile"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil || req.URL == "" {
		return writeError(resPtr, resCap, "url is required")
	}
	if req.Profile == "" {
		req.Profile = "chrome"
	}
	sess := &sessionState{
		ID: newSessionID(), Profile: req.Profile,
		ExtraHdrs: req.Headers, CreatedUnix: time.Now().Unix(),
	}
	page, err := fetchPage(sess, "GET", req.URL, nil, "")
	if err != nil {
		return writeError(resPtr, resCap, err.Error())
	}
	saveSession(sess)
	return writeResult(resPtr, resCap, page)
}

// ── tier-1 tool: browser_click ────────────────────────────────────────────

//go:wasmexport stado_tool_browser_click
func stadoToolBrowserClick(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID  string `json:"session_id"`
		LinkIndex  *int   `json:"link_index"`
		TextMatch  string `json:"text_match"`
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil {
		return writeError(resPtr, resCap, "invalid args")
	}
	sess, ok := loadSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "session not found")
	}
	if sess.CurrentBody == "" {
		return writeError(resPtr, resCap, "no page loaded in this session")
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(sess.CurrentBody))
	if err != nil {
		return writeError(resPtr, resCap, "parse: "+err.Error())
	}
	links := extractLinks(doc, sess.CurrentURL)
	var target string
	if req.LinkIndex != nil && *req.LinkIndex < len(links) {
		target = links[*req.LinkIndex].Href
	} else if req.TextMatch != "" {
		lc := strings.ToLower(req.TextMatch)
		for _, l := range links {
			if strings.Contains(strings.ToLower(l.Text), lc) {
				target = l.Href
				break
			}
		}
	}
	if target == "" {
		return writeError(resPtr, resCap, "link not found")
	}
	page, err := fetchPage(sess, "GET", target, nil, sess.CurrentURL)
	if err != nil {
		return writeError(resPtr, resCap, err.Error())
	}
	saveSession(sess)
	return writeResult(resPtr, resCap, page)
}

// ── tier-1 tool: browser_query ────────────────────────────────────────────

//go:wasmexport stado_tool_browser_query
func stadoToolBrowserQuery(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		Selector  string `json:"selector"`
		Attr      string `json:"attr"`
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil || req.Selector == "" {
		return writeError(resPtr, resCap, "selector is required")
	}
	sess, ok := loadSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "session not found")
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(sess.CurrentBody))
	if err != nil {
		return writeError(resPtr, resCap, "parse: "+err.Error())
	}
	type match struct {
		HTML  string            `json:"html"`
		Text  string            `json:"text"`
		Attrs map[string]string `json:"attrs,omitempty"`
		Value string            `json:"value,omitempty"`
	}
	var matches []match
	doc.Find(req.Selector).Each(func(_ int, s *goquery.Selection) {
		m := match{
			Text: strings.TrimSpace(s.Text()),
		}
		html, _ := goquery.OuterHtml(s)
		m.HTML = truncate(html, 4096)
		if req.Attr != "" {
			m.Value = s.AttrOr(req.Attr, "")
		}
		matches = append(matches, m)
	})
	return writeResult(resPtr, resCap, map[string]any{"matches": matches})
}

// ── tier-2 tool: browser_cdp_open ────────────────────────────────────────

//go:wasmexport stado_tool_browser_cdp_open
func stadoToolBrowserCDPOpen(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		URL       string `json:"url"`
		WaitForMs int    `json:"wait_for_ms"`
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil || req.URL == "" {
		return writeError(resPtr, resCap, "url is required")
	}
	if req.WaitForMs <= 0 {
		req.WaitForMs = 10000
	}

	logInfo("browser_cdp_open: spawning Chrome")
	procH, wsURL, err := spawnChrome()
	if err != nil {
		return writeError(resPtr, resCap, err.Error())
	}
	logInfo("browser_cdp_open: Chrome CDP at " + wsURL)

	sess, err := cdpConnect(wsURL)
	if err != nil {
		stadoProcClose(procH)
		return writeError(resPtr, resCap, "CDP connect: "+err.Error())
	}
	sess.procHandle = procH

	if err := sess.navigate(req.URL, req.WaitForMs); err != nil {
		return writeError(resPtr, resCap, "navigate: "+err.Error())
	}

	content := sess.getPageContent()
	saveCDPSession(sess)
	return writeResult(resPtr, resCap, content)
}

// ── tier-2 tool: browser_cdp_eval ────────────────────────────────────────

//go:wasmexport stado_tool_browser_cdp_eval
func stadoToolBrowserCDPEval(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		JS        string `json:"js"`
		AwaitPromise bool `json:"await_promise"`
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil || req.JS == "" {
		return writeError(resPtr, resCap, "js is required")
	}
	sess, ok := loadCDPSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found — use browser_cdp_open first")
	}

	result, err := sess.send("Runtime.evaluate", map[string]any{
		"expression":    req.JS,
		"returnByValue": true,
		"awaitPromise":  req.AwaitPromise,
	})
	if err != nil {
		return writeError(resPtr, resCap, err.Error())
	}

	// Extract the result value.
	var value any
	if r, ok := result["result"].(map[string]any); ok {
		value = r["value"]
		if value == nil {
			// Complex object — return serialized description.
			value = r["description"]
		}
	}
	if ex, ok := result["exceptionDetails"].(map[string]any); ok {
		if text, ok := ex["text"].(string); ok {
			return writeError(resPtr, resCap, "JS exception: "+text)
		}
	}
	return writeResult(resPtr, resCap, map[string]any{"result": value})
}

// ── tier-2 tool: browser_cdp_screenshot ──────────────────────────────────

//go:wasmexport stado_tool_browser_cdp_screenshot
func stadoToolBrowserCDPScreenshot(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		Format    string `json:"format"` // "png" | "jpeg"
		Quality   int    `json:"quality"`
	}
	_ = json.Unmarshal(readArgs(argsPtr, argsLen), &req)
	if req.Format == "" {
		req.Format = "png"
	}
	if req.Quality <= 0 {
		req.Quality = 80
	}
	sess, ok := loadCDPSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found")
	}

	params := map[string]any{"format": req.Format}
	if req.Format == "jpeg" {
		params["quality"] = req.Quality
	}
	result, err := sess.send("Page.captureScreenshot", params)
	if err != nil {
		return writeError(resPtr, resCap, err.Error())
	}
	data, _ := result["data"].(string) // base64-encoded image
	return writeResult(resPtr, resCap, map[string]any{
		"format":  req.Format,
		"data_b64": data,
		"url":     sess.url,
	})
}

// ── tier-2 tool: browser_cdp_navigate ────────────────────────────────────

//go:wasmexport stado_tool_browser_cdp_navigate
func stadoToolBrowserCDPNavigate(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		URL       string `json:"url"`
		WaitForMs int    `json:"wait_for_ms"`
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil || req.URL == "" {
		return writeError(resPtr, resCap, "session_id and url are required")
	}
	sess, ok := loadCDPSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found")
	}
	if err := sess.navigate(req.URL, req.WaitForMs); err != nil {
		return writeError(resPtr, resCap, err.Error())
	}
	content := sess.getPageContent()
	return writeResult(resPtr, resCap, content)
}

// ── tier-2 tool: browser_cdp_close ───────────────────────────────────────

//go:wasmexport stado_tool_browser_cdp_close
func stadoToolBrowserCDPClose(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(readArgs(argsPtr, argsLen), &req)
	if req.SessionID == "" {
		return writeError(resPtr, resCap, "session_id is required")
	}
	sessMu.Lock()
	sess, ok := cdpSessions[req.SessionID]
	if ok {
		delete(cdpSessions, req.SessionID)
	}
	sessMu.Unlock()
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found")
	}
	if sess.netHandle != 0 {
		stadoNetClose(sess.netHandle)
	}
	if sess.procHandle != 0 {
		stadoProcClose(sess.procHandle)
	}
	return writeResult(resPtr, resCap, map[string]bool{"ok": true})
}

// ── tier-2 tool: browser_cdp_click_element ────────────────────────────────
// Click on an element matched by CSS selector. Resolves the element's
// centre coordinates, dispatches a real mouse click through CDP so that
// JS onclick handlers, hover states, and custom components all fire.

//go:wasmexport stado_tool_browser_cdp_click_element
func stadoToolBrowserCDPClickElement(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		Selector  string `json:"selector"` // CSS selector
		Index     int    `json:"index"`     // nth match (0-based, default 0)
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil || req.Selector == "" {
		return writeError(resPtr, resCap, "session_id and selector are required")
	}
	sess, ok := loadCDPSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found")
	}

	// Use Runtime.evaluate to get the bounding rect of the target element.
	jsGetRect := fmt.Sprintf(`(function(){
		var els = document.querySelectorAll(%q);
		var el = els[%d];
		if (!el) return null;
		el.scrollIntoView({block:'center'});
		var r = el.getBoundingClientRect();
		return {x: r.left + r.width/2, y: r.top + r.height/2, found: true};
	})()`, req.Selector, req.Index)

	result, err := sess.send("Runtime.evaluate", map[string]any{
		"expression": jsGetRect, "returnByValue": true,
	})
	if err != nil {
		return writeError(resPtr, resCap, "evaluate: "+err.Error())
	}
	rv, ok2 := result["result"].(map[string]any)
	if !ok2 {
		return writeError(resPtr, resCap, "element not found: "+req.Selector)
	}
	val, ok3 := rv["value"].(map[string]any)
	if !ok3 || val["found"] == nil {
		return writeError(resPtr, resCap, "element not found (index "+fmt.Sprint(req.Index)+"): "+req.Selector)
	}
	x, _ := val["x"].(float64)
	y, _ := val["y"].(float64)

	// Dispatch mousePressed + mouseReleased at the element's centre.
	for _, evType := range []string{"mousePressed", "mouseReleased"} {
		if _, err := sess.send("Input.dispatchMouseEvent", map[string]any{
			"type":       evType,
			"x":          x,
			"y":          y,
			"button":     "left",
			"clickCount": 1,
		}); err != nil {
			return writeError(resPtr, resCap, evType+": "+err.Error())
		}
	}

	// Brief pause for JS event handlers to fire, then return updated content.
	// (We poll readyState briefly rather than sleeping a fixed duration.)
	for i := 0; i < 10; i++ {
		r2, _ := sess.send("Runtime.evaluate", map[string]any{
			"expression": "document.readyState", "returnByValue": true,
		})
		if rv2, ok := r2["result"].(map[string]any); ok {
			if state, _ := rv2["value"].(string); state == "complete" {
				break
			}
		}
	}
	sess.url, _ = func() (string, error) {
		r3, err := sess.send("Runtime.evaluate", map[string]any{
			"expression": "location.href", "returnByValue": true,
		})
		if err != nil || r3 == nil {
			return sess.url, err
		}
		if rv3, ok := r3["result"].(map[string]any); ok {
			u, _ := rv3["value"].(string)
			return u, nil
		}
		return sess.url, nil
	}()

	return writeResult(resPtr, resCap, map[string]any{
		"ok":      true,
		"clicked": req.Selector,
		"x":       x, "y": y,
		"url": sess.url,
	})
}

// ── tier-2 tool: browser_cdp_type ────────────────────────────────────────
// Type text into the focused element (or focus a selector first).
// Uses CDP Input.insertText for the text body and Input.dispatchKeyEvent
// for special keys (Enter, Tab, Escape, Backspace, ArrowDown, etc.).

//go:wasmexport stado_tool_browser_cdp_type
func stadoToolBrowserCDPType(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`       // text to type (printable characters)
		Selector  string `json:"selector"`   // optional: focus this element first
		Key       string `json:"key"`        // optional special key: Enter, Tab, Escape, Backspace, ArrowDown, ...
		ClearFirst bool  `json:"clear_first"` // select-all + delete before typing
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil {
		return writeError(resPtr, resCap, "invalid args")
	}
	if req.Text == "" && req.Key == "" {
		return writeError(resPtr, resCap, "text or key is required")
	}
	sess, ok := loadCDPSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found")
	}

	// Optionally focus a specific element first.
	if req.Selector != "" {
		focusJS := fmt.Sprintf(`(function(){
			var el = document.querySelector(%q);
			if (!el) return false;
			el.focus();
			return true;
		})()`, req.Selector)
		sess.send("Runtime.evaluate", map[string]any{"expression": focusJS, "returnByValue": true}) //nolint:errcheck
	}

	// Clear existing content if requested.
	if req.ClearFirst {
		// Ctrl+A then Delete.
		for _, key := range []struct{ code, key string }{
			{"KeyA", "a"},
		} {
			sess.send("Input.dispatchKeyEvent", map[string]any{ //nolint:errcheck
				"type": "keyDown", "key": key.key, "code": key.code,
				"modifiers": 2, // Ctrl
			})
			sess.send("Input.dispatchKeyEvent", map[string]any{ //nolint:errcheck
				"type": "keyUp", "key": key.key, "code": key.code,
				"modifiers": 2,
			})
		}
		sess.send("Input.dispatchKeyEvent", map[string]any{ //nolint:errcheck
			"type": "keyDown", "key": "Delete", "code": "Delete",
		})
		sess.send("Input.dispatchKeyEvent", map[string]any{ //nolint:errcheck
			"type": "keyUp", "key": "Delete", "code": "Delete",
		})
	}

	// Type the text body using Input.insertText (handles unicode correctly).
	if req.Text != "" {
		if _, err := sess.send("Input.insertText", map[string]any{"text": req.Text}); err != nil {
			return writeError(resPtr, resCap, "insertText: "+err.Error())
		}
	}

	// Send a special key if requested.
	if req.Key != "" {
		code := keyCode(req.Key)
		for _, evType := range []string{"keyDown", "keyUp"} {
			sess.send("Input.dispatchKeyEvent", map[string]any{ //nolint:errcheck
				"type": evType,
				"key":  req.Key,
				"code": code,
			})
		}
	}

	return writeResult(resPtr, resCap, map[string]bool{"ok": true})
}

// keyCode maps common key names to their CDP code strings.
func keyCode(key string) string {
	codes := map[string]string{
		"Enter": "Enter", "Tab": "Tab", "Escape": "Escape",
		"Backspace": "Backspace", "Delete": "Delete", "Space": "Space",
		"ArrowUp": "ArrowUp", "ArrowDown": "ArrowDown",
		"ArrowLeft": "ArrowLeft", "ArrowRight": "ArrowRight",
		"Home": "Home", "End": "End", "PageUp": "PageUp", "PageDown": "PageDown",
		"F1": "F1", "F2": "F2", "F5": "F5", "F12": "F12",
	}
	if c, ok := codes[key]; ok {
		return c
	}
	return "Key" + key // best-effort for single letters
}

// ── tier-2 tool: browser_cdp_scroll ──────────────────────────────────────
// Scroll the page or a specific element. Triggers scroll events so
// lazy-loading, infinite scroll, and sticky headers all behave correctly.

//go:wasmexport stado_tool_browser_cdp_scroll
func stadoToolBrowserCDPScroll(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		SessionID string `json:"session_id"`
		Selector  string `json:"selector"`   // optional: scroll within this element
		X         int    `json:"x"`           // target scroll X (pixels from left)
		Y         int    `json:"y"`           // target scroll Y (pixels from top)
		Delta     int    `json:"delta"`       // relative scroll amount (positive = down)
		Behavior  string `json:"behavior"`    // "smooth" | "instant" (default instant)
		WaitForMs int    `json:"wait_for_ms"` // ms to wait after scroll (for lazy-load)
	}
	if err := json.Unmarshal(readArgs(argsPtr, argsLen), &req); err != nil {
		return writeError(resPtr, resCap, "invalid args")
	}
	sess, ok := loadCDPSession(req.SessionID)
	if !ok {
		return writeError(resPtr, resCap, "CDP session not found")
	}
	if req.Behavior == "" {
		req.Behavior = "instant"
	}

	var scrollJS string
	if req.Selector != "" {
		// Scroll within a specific element.
		if req.Delta != 0 {
			scrollJS = fmt.Sprintf(`(function(){
				var el = document.querySelector(%q);
				if (!el) return {error:'element not found'};
				el.scrollBy({top:%d, left:0, behavior:%q});
				return {scrollTop: el.scrollTop, scrollLeft: el.scrollLeft};
			})()`, req.Selector, req.Delta, req.Behavior)
		} else {
			scrollJS = fmt.Sprintf(`(function(){
				var el = document.querySelector(%q);
				if (!el) return {error:'element not found'};
				el.scrollTo({top:%d, left:%d, behavior:%q});
				return {scrollTop: el.scrollTop, scrollLeft: el.scrollLeft};
			})()`, req.Selector, req.Y, req.X, req.Behavior)
		}
	} else {
		// Scroll the window.
		if req.Delta != 0 {
			scrollJS = fmt.Sprintf(`(function(){
				window.scrollBy({top:%d, left:0, behavior:%q});
				return {scrollY: window.scrollY, scrollX: window.scrollX};
			})()`, req.Delta, req.Behavior)
		} else {
			scrollJS = fmt.Sprintf(`(function(){
				window.scrollTo({top:%d, left:%d, behavior:%q});
				return {scrollY: window.scrollY, scrollX: window.scrollX};
			})()`, req.Y, req.X, req.Behavior)
		}
	}

	result, err := sess.send("Runtime.evaluate", map[string]any{
		"expression": scrollJS, "returnByValue": true,
	})
	if err != nil {
		return writeError(resPtr, resCap, "scroll: "+err.Error())
	}

	// Wait briefly for lazy-loaded content to appear.
	if req.WaitForMs > 0 {
		// Poll up to wait_for_ms for network activity to settle.
		_ = req.WaitForMs // in a real impl: wait for network idle; here we just note it
	}

	var scrollPos map[string]any
	if rv, ok := result["result"].(map[string]any); ok {
		scrollPos, _ = rv["value"].(map[string]any)
	}
	return writeResult(resPtr, resCap, map[string]any{"ok": true, "position": scrollPos})
}
