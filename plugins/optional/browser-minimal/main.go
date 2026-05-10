// browser — a tiny headless "browser" for stado plugins. It fetches
// pages, parses the DOM, navigates links, submits forms, persists
// cookies, and spoofs UA / Accept-* headers so a server-side
// fingerprint check sees a plausible Chrome/Firefox/Safari.
//
// Read this first: it is NOT a real browser.
//
//   - No JavaScript execution. Rationale: a JS engine inside a wasip1
//     plugin would be Goja or QuickJS; either adds 1-3 MB to the wasm
//     and gives no pixel/canvas, no WebGL, no AudioContext. Modern
//     anti-bot (Cloudflare 2026+) decides at the TLS layer (JA3/JA4)
//     and the canvas/WebGL layer — neither of which a plugin can
//     touch. JS would only help against sites whose data is gated
//     behind client-side rendering, and those are not the v0.1 target.
//   - No rendering. "screenshot" returns the serialized DOM plus a
//     flat text extract. Useful for an LLM walking the page; useless
//     for a human looking at pixels.
//   - No real fingerprint resistance. We set User-Agent, Accept-*,
//     Accept-Language, Sec-Ch-Ua, and a Cookie header to match a
//     plausible Chrome/Firefox/Safari. JA3/JA4 still come from the
//     host's HTTP client. If the host doesn't spoof those, anti-bot
//     systems will catch this plugin on the first handshake.
//
// Tools:
//
//   browser_open {url, profile?, headers?}
//     → {session_id, url, status, headers, title, links, forms, cookies, text, html}
//
//   browser_click {session_id, link_index?, selector?, text_match?}
//     → same shape as browser_open
//
//   browser_submit {session_id, form_index?, selector?, fields, click?}
//     → same shape as browser_open
//
//   browser_screenshot {session_id}
//     → {url, title, text, html, dom_summary}
//
//   browser_eval {session_id, selector, attr?}
//     → {matches: [{html, text, attrs}]}
//       Lets a caller fish a value out of the current page without
//       another round-trip. attr=null returns text + outerHTML.
//
// "profile" picks a header bundle: chrome (default), firefox, safari.
//
// Capabilities:
//   - net:http_request
//   - net:http_request_private  (optional — drop if you only hit
//                                public sites; keeps strict dial guard)
//   - fs:read:.cache/stado-browser
//   - fs:write:.cache/stado-browser
//
// Operator-side setup:
//
//   mkdir -p <workdir>/.cache/stado-browser
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
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
	cacheDir   = ".cache/stado-browser"
	httpBufCap = 8 << 20
	maxRedirs  = 10
)

// ---- session state -----------------------------------------------------

type sessionState struct {
	ID          string                       `json:"id"`
	Profile     string                       `json:"profile"`           // chrome | firefox | safari
	ExtraHdrs   map[string]string            `json:"extra_headers,omitempty"`
	Cookies     map[string]map[string]string `json:"cookies,omitempty"` // host → name → value
	CurrentURL  string                       `json:"current_url,omitempty"`
	CurrentBody string                       `json:"current_body,omitempty"`
	CreatedUnix int64                        `json:"created_unix"`
}

type errResult struct {
	Error string `json:"error"`
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
	Index    int         `json:"index"`
	Action   string      `json:"action"`
	Method   string      `json:"method"`
	Selector string      `json:"selector,omitempty"`
	Fields   []formField `json:"fields,omitempty"`
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
}

// ---- host HTTP types ---------------------------------------------------

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

// ---- profiles (UA + Accept-* bundles) ---------------------------------
//
// These match recent stable channels (May 2026). We don't try to be
// exhaustive — just enough for a server doing simple UA-string sniffing
// to see "this is a browser, not a script". A determined fingerprinter
// (canvas, WebGL, JA3) sees through this immediately.

// We deliberately omit Accept-Encoding from every profile. The host's
// httpreq tool runs on Go's net/http transport, which auto-injects
// Accept-Encoding: gzip and transparently decompresses the response —
// but only when the caller hasn't set the header. Setting it ourselves
// ("br, zstd, ...") would force the transport to leave the body
// compressed and the plugin would have to ship a brotli/zstd decoder.
// Net effect of skipping Accept-Encoding: gzip-decompressed body, no
// dependency on a wasm brotli port. Cost in fingerprint fidelity is
// minor — modern fingerprinters look at TLS, not Accept-Encoding.
var profiles = map[string]map[string]string{
	"chrome": {
		"User-Agent":                "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.9",
		"Sec-Ch-Ua":                 `"Chromium";v="130", "Not(A:Brand";v="99", "Google Chrome";v="130"`,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Linux"`,
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	},
	"firefox": {
		"User-Agent":                "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.5",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
	},
	"safari": {
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	},
}

// ---- tool: browser_open ------------------------------------------------

type openArgs struct {
	URL     string            `json:"url"`
	Profile string            `json:"profile,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

//go:wasmexport stado_tool_browser_open
func stadoToolBrowserOpen(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("browser_open invoked")

	var a openArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	if strings.TrimSpace(a.URL) == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "url is required"})
	}
	profile := strings.ToLower(strings.TrimSpace(a.Profile))
	if profile == "" {
		profile = "chrome"
	}
	if _, ok := profiles[profile]; !ok {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "unknown profile: " + profile + " (try chrome, firefox, safari)",
		})
	}

	sess := &sessionState{
		ID:          newSessionID(a.URL),
		Profile:     profile,
		ExtraHdrs:   a.Headers,
		Cookies:     map[string]map[string]string{},
		CreatedUnix: time.Now().Unix(),
	}

	res, err := navigate(sess, "GET", a.URL, nil, "")
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if err := saveSession(sess); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: err.Error() + " — ensure mkdir -p " + cacheDir + " in workdir",
		})
	}
	return writeJSON(resultPtr, resultCap, res)
}

// ---- tool: browser_click -----------------------------------------------

type clickArgs struct {
	SessionID string `json:"session_id"`
	LinkIndex *int   `json:"link_index,omitempty"`
	Selector  string `json:"selector,omitempty"`
	TextMatch string `json:"text_match,omitempty"`
}

//go:wasmexport stado_tool_browser_click
func stadoToolBrowserClick(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("browser_click invoked")

	var a clickArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	sess, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if sess.CurrentBody == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session has no current page (call browser_open first)"})
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(sess.CurrentBody))
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "parse current page: " + err.Error()})
	}

	href, found := pickLinkHref(doc, a.LinkIndex, a.Selector, a.TextMatch)
	if !found {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "no matching link (try browser_screenshot to inspect available links)",
		})
	}
	target, err := resolveURL(sess.CurrentURL, href)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "resolve href: " + err.Error()})
	}

	res, err := navigate(sess, "GET", target, nil, sess.CurrentURL)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if err := saveSession(sess); err != nil {
		logInfo("save session: " + err.Error())
	}
	return writeJSON(resultPtr, resultCap, res)
}

// ---- tool: browser_submit ----------------------------------------------

type submitArgs struct {
	SessionID string            `json:"session_id"`
	FormIndex *int              `json:"form_index,omitempty"`
	Selector  string            `json:"selector,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	Click     string            `json:"click,omitempty"` // name of submit button to "click" (sets that name=value pair)
}

//go:wasmexport stado_tool_browser_submit
func stadoToolBrowserSubmit(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("browser_submit invoked")

	var a submitArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	sess, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if sess.CurrentBody == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session has no current page (call browser_open first)"})
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(sess.CurrentBody))
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "parse current page: " + err.Error()})
	}

	form, found := pickForm(doc, a.FormIndex, a.Selector)
	if !found {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "no matching form (try browser_screenshot to inspect available forms)",
		})
	}

	method := strings.ToUpper(strings.TrimSpace(form.AttrOr("method", "GET")))
	if method != "POST" {
		method = "GET"
	}
	action := form.AttrOr("action", sess.CurrentURL)
	target, err := resolveURL(sess.CurrentURL, action)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "resolve action: " + err.Error()})
	}
	enctype := strings.ToLower(form.AttrOr("enctype", ""))
	if enctype != "" && !strings.Contains(enctype, "application/x-www-form-urlencoded") {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "unsupported enctype: " + enctype + " (only application/x-www-form-urlencoded for v0.1)",
		})
	}

	values := harvestFormDefaults(form)
	for k, v := range a.Fields {
		values.Set(k, v)
	}
	if a.Click != "" {
		// Most submit buttons don't have a fixed default value, but if
		// caller specified a button name we record it as a key with
		// whatever value the button defines (or "1" if none).
		btnVal := pickSubmitValue(form, a.Click)
		values.Set(a.Click, btnVal)
	}

	encoded := values.Encode()

	var body string
	method2 := method
	switch method2 {
	case "POST":
		body = encoded
	default:
		// GET: append to URL.
		if strings.Contains(target, "?") {
			target += "&" + encoded
		} else if encoded != "" {
			target += "?" + encoded
		}
	}

	res, err := navigate(sess, method2, target, []byte(body), sess.CurrentURL)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if err := saveSession(sess); err != nil {
		logInfo("save session: " + err.Error())
	}
	return writeJSON(resultPtr, resultCap, res)
}

// ---- tool: browser_screenshot ------------------------------------------

type screenshotArgs struct {
	SessionID string `json:"session_id"`
}

type screenshotResult struct {
	URL        string    `json:"url"`
	Title      string    `json:"title,omitempty"`
	Text       string    `json:"text"`
	HTML       string    `json:"html"`
	DOMSummary string    `json:"dom_summary"`
	Links      []linkOut `json:"links,omitempty"`
	Forms      []formOut `json:"forms,omitempty"`
}

//go:wasmexport stado_tool_browser_screenshot
func stadoToolBrowserScreenshot(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("browser_screenshot invoked")

	var a screenshotArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	sess, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if sess.CurrentBody == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session has no current page"})
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(sess.CurrentBody))
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "parse: " + err.Error()})
	}

	res := screenshotResult{
		URL:        sess.CurrentURL,
		Title:      strings.TrimSpace(doc.Find("title").First().Text()),
		Text:       extractText(doc),
		HTML:       sess.CurrentBody,
		DOMSummary: domSummary(doc),
		Links:      extractLinks(doc, sess.CurrentURL),
		Forms:      extractForms(doc, sess.CurrentURL),
	}
	return writeJSON(resultPtr, resultCap, res)
}

// ---- tool: browser_eval ------------------------------------------------

type evalArgs struct {
	SessionID string `json:"session_id"`
	Selector  string `json:"selector"`
	Attr      string `json:"attr,omitempty"`
}

type evalMatch struct {
	HTML  string            `json:"html,omitempty"`
	Text  string            `json:"text,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
	Value string            `json:"value,omitempty"`
}

type evalResult struct {
	Matches []evalMatch `json:"matches"`
}

//go:wasmexport stado_tool_browser_eval
func stadoToolBrowserEval(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("browser_eval invoked")

	var a evalArgs
	if err := unmarshalArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
	}
	if strings.TrimSpace(a.Selector) == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "selector is required"})
	}
	sess, err := loadSession(a.SessionID)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if sess.CurrentBody == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "session has no current page"})
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(sess.CurrentBody))
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "parse: " + err.Error()})
	}

	var out evalResult
	doc.Find(a.Selector).Each(func(_ int, s *goquery.Selection) {
		m := evalMatch{
			Text:  strings.TrimSpace(s.Text()),
			Attrs: nodeAttrs(s),
		}
		if html, err := goquery.OuterHtml(s); err == nil {
			m.HTML = truncate(html, 4096)
		}
		if a.Attr != "" {
			if v, ok := s.Attr(a.Attr); ok {
				m.Value = v
			}
		}
		out.Matches = append(out.Matches, m)
	})
	return writeJSON(resultPtr, resultCap, out)
}

// ---- core: navigate ----------------------------------------------------

// navigate performs an HTTP request (with redirect handling) and updates
// the session's current page + cookie jar.
func navigate(sess *sessionState, method, target string, body []byte, referer string) (*pageResult, error) {
	for hop := 0; hop < maxRedirs; hop++ {
		req := buildRequest(sess, method, target, body, referer)
		respHdrs, status, respBody, err := doHTTP(req)
		if err != nil {
			return nil, err
		}
		harvestSetCookie(sess, target, respHdrs)
		if isRedirect(status) {
			loc := pickHeader(respHdrs, "Location")
			if loc == "" {
				return nil, fmt.Errorf("HTTP %d redirect with no Location header", status)
			}
			next, err := resolveURL(target, loc)
			if err != nil {
				return nil, err
			}
			referer = target
			target = next
			method = "GET"
			body = nil
			continue
		}

		sess.CurrentURL = target
		sess.CurrentBody = string(respBody)

		// Build response.
		doc, _ := goquery.NewDocumentFromReader(bytes.NewReader(respBody))
		title := ""
		if doc != nil {
			title = strings.TrimSpace(doc.Find("title").First().Text())
		}
		res := &pageResult{
			SessionID: sess.ID,
			URL:       target,
			Status:    status,
			Headers:   respHdrs,
			Title:     title,
			Cookies:   sess.Cookies[hostOf(target)],
		}
		if doc != nil {
			res.Links = extractLinks(doc, target)
			res.Forms = extractForms(doc, target)
			res.Text = truncate(extractText(doc), 32768)
			res.HTML = truncate(string(respBody), 65536)
		} else {
			res.HTML = truncate(string(respBody), 65536)
		}
		return res, nil
	}
	return nil, fmt.Errorf("too many redirects (%d)", maxRedirs)
}

func buildRequest(sess *sessionState, method, target string, body []byte, referer string) hostHTTPRequest {
	headers := map[string]string{}
	for k, v := range profiles[sess.Profile] {
		headers[k] = v
	}
	for k, v := range sess.ExtraHdrs {
		headers[k] = v
	}
	if referer != "" {
		headers["Referer"] = referer
		// On a same-site nav, switch Sec-Fetch-Site to "same-origin"
		// for the chrome profile. Cheap fidelity boost.
		if sess.Profile == "chrome" && sameHost(referer, target) {
			headers["Sec-Fetch-Site"] = "same-origin"
		}
	}
	if cookieHdr := buildCookieHeader(sess, target); cookieHdr != "" {
		headers["Cookie"] = cookieHdr
	}
	if method == "POST" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	req := hostHTTPRequest{
		Method:    method,
		URL:       target,
		Headers:   headers,
		TimeoutMs: 30000,
	}
	if len(body) > 0 {
		req.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}
	return req
}

func doHTTP(req hostHTTPRequest) (map[string]string, int, []byte, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, 0, nil, err
	}
	scratch := make([]byte, httpBufCap)
	n := stadoHttpRequest(
		uint32(uintptr(unsafe.Pointer(&reqBytes[0]))), uint32(len(reqBytes)),
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(httpBufCap),
	)
	if n < 0 {
		return nil, 0, nil, fmt.Errorf("stado_http_request: %s", string(scratch[:-n]))
	}
	var resp hostHTTPResponse
	if err := json.Unmarshal(scratch[:n], &resp); err != nil {
		return nil, 0, nil, fmt.Errorf("decode response: %w", err)
	}
	body, err := base64.StdEncoding.DecodeString(resp.BodyB64)
	if err != nil {
		return resp.Headers, resp.Status, nil, fmt.Errorf("decode body_b64: %w", err)
	}
	return resp.Headers, resp.Status, body, nil
}

func isRedirect(status int) bool {
	return status >= 300 && status < 400 && status != 304
}

// ---- cookie jar -------------------------------------------------------

func harvestSetCookie(sess *sessionState, target string, headers map[string]string) {
	host := hostOf(target)
	if host == "" {
		return
	}
	sc := pickHeader(headers, "Set-Cookie")
	if sc == "" {
		return
	}
	if sess.Cookies[host] == nil {
		sess.Cookies[host] = map[string]string{}
	}
	for _, c := range splitSetCookie(sc) {
		if name, val, ok := parseCookiePair(c); ok {
			sess.Cookies[host][name] = val
		}
	}
}

func buildCookieHeader(sess *sessionState, target string) string {
	host := hostOf(target)
	jar := sess.Cookies[host]
	if len(jar) == 0 {
		return ""
	}
	var parts []string
	for name, val := range jar {
		parts = append(parts, name+"="+val)
	}
	return strings.Join(parts, "; ")
}

func splitSetCookie(combined string) []string {
	// stado's host folds multi-value headers comma-joined. Split on
	// ", " only when followed by a `name=` token (heuristic).
	parts := []string{}
	cur := ""
	tokens := strings.Split(combined, ", ")
	for i, tok := range tokens {
		if cur == "" {
			cur = tok
			continue
		}
		// If the next token starts with a likely cookie name, the prior
		// "; " was a delimiter.
		if i+1 < len(tokens) && cookieNameLooksReal(tok) {
			parts = append(parts, cur)
			cur = tok
		} else {
			cur += ", " + tok
		}
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
}

func cookieNameLooksReal(s string) bool {
	idx := strings.Index(s, "=")
	if idx <= 0 {
		return false
	}
	name := s[:idx]
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func parseCookiePair(c string) (string, string, bool) {
	end := len(c)
	if semi := strings.Index(c, ";"); semi >= 0 {
		end = semi
	}
	first := c[:end]
	idx := strings.Index(first, "=")
	if idx <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(first[:idx]), strings.TrimSpace(first[idx+1:]), true
}

// ---- DOM helpers ------------------------------------------------------

func extractLinks(doc *goquery.Document, base string) []linkOut {
	var out []linkOut
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if strings.TrimSpace(href) == "" || strings.HasPrefix(href, "javascript:") {
			return
		}
		resolved := href
		if abs, err := resolveURL(base, href); err == nil {
			resolved = abs
		}
		text := strings.TrimSpace(squashWS(s.Text()))
		out = append(out, linkOut{Index: i, Href: resolved, Text: truncate(text, 120)})
	})
	return out
}

func extractForms(doc *goquery.Document, base string) []formOut {
	var out []formOut
	doc.Find("form").Each(func(i int, s *goquery.Selection) {
		action := s.AttrOr("action", base)
		if abs, err := resolveURL(base, action); err == nil {
			action = abs
		}
		method := strings.ToUpper(s.AttrOr("method", "GET"))
		var fields []formField
		s.Find("input,select,textarea").Each(func(_ int, el *goquery.Selection) {
			name, _ := el.Attr("name")
			if name == "" {
				return
			}
			t := strings.ToLower(el.AttrOr("type", "text"))
			if t == "submit" || t == "button" || t == "reset" {
				return
			}
			val, _ := el.Attr("value")
			fields = append(fields, formField{Name: name, Type: t, Value: val})
		})
		out = append(out, formOut{
			Index:    i,
			Action:   action,
			Method:   method,
			Selector: fmt.Sprintf("form:nth-of-type(%d)", i+1),
			Fields:   fields,
		})
	})
	return out
}

func extractText(doc *goquery.Document) string {
	// Drop script/style content before flattening.
	doc.Find("script,style,noscript").Remove()
	body := doc.Find("body").First()
	if body.Length() == 0 {
		body = doc.Selection
	}
	raw := body.Text()
	return strings.TrimSpace(squashWS(raw))
}

func domSummary(doc *goquery.Document) string {
	// One-line summary: counts of common tags. Useful for an LLM
	// deciding whether to fetch full HTML.
	counts := []struct {
		tag   string
		label string
	}{
		{"a", "links"}, {"img", "images"}, {"form", "forms"},
		{"input", "inputs"}, {"button", "buttons"}, {"script", "scripts"},
		{"iframe", "iframes"}, {"table", "tables"},
	}
	var parts []string
	for _, c := range counts {
		n := doc.Find(c.tag).Length()
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, c.label))
		}
	}
	if len(parts) == 0 {
		return "empty document"
	}
	return strings.Join(parts, ", ")
}

func nodeAttrs(s *goquery.Selection) map[string]string {
	out := map[string]string{}
	for _, n := range s.Nodes {
		for _, a := range n.Attr {
			out[a.Key] = a.Val
		}
		break // first node only
	}
	return out
}

func pickLinkHref(doc *goquery.Document, idx *int, selector, textMatch string) (string, bool) {
	if idx != nil {
		i := *idx
		links := doc.Find("a[href]")
		if i < 0 || i >= links.Length() {
			return "", false
		}
		return links.Eq(i).AttrOr("href", ""), true
	}
	if selector != "" {
		s := doc.Find(selector).First()
		if s.Length() == 0 {
			return "", false
		}
		return s.AttrOr("href", ""), true
	}
	if textMatch != "" {
		needle := strings.ToLower(textMatch)
		var picked string
		doc.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
			if strings.Contains(strings.ToLower(s.Text()), needle) {
				picked = s.AttrOr("href", "")
				return false
			}
			return true
		})
		return picked, picked != ""
	}
	return "", false
}

func pickForm(doc *goquery.Document, idx *int, selector string) (*goquery.Selection, bool) {
	if selector != "" {
		s := doc.Find(selector).First()
		if s.Length() == 0 {
			return nil, false
		}
		return s, true
	}
	if idx != nil {
		forms := doc.Find("form")
		if *idx < 0 || *idx >= forms.Length() {
			return nil, false
		}
		s := forms.Eq(*idx)
		return s, true
	}
	// Default: first form.
	first := doc.Find("form").First()
	if first.Length() == 0 {
		return nil, false
	}
	return first, true
}

func harvestFormDefaults(form *goquery.Selection) url.Values {
	v := url.Values{}
	form.Find("input,select,textarea").Each(func(_ int, el *goquery.Selection) {
		name, _ := el.Attr("name")
		if name == "" {
			return
		}
		t := strings.ToLower(el.AttrOr("type", "text"))
		switch t {
		case "submit", "button", "reset":
			return
		case "checkbox", "radio":
			if _, checked := el.Attr("checked"); checked {
				val := el.AttrOr("value", "on")
				v.Set(name, val)
			}
			return
		}
		val, _ := el.Attr("value")
		// textarea text is in body, not value attr
		if t == "textarea" || el.Get(0).Data == "textarea" {
			val = el.Text()
		}
		v.Set(name, val)
	})
	return v
}

func pickSubmitValue(form *goquery.Selection, name string) string {
	val := "1"
	form.Find(`input[type="submit"], button[type="submit"], button:not([type])`).EachWithBreak(func(_ int, el *goquery.Selection) bool {
		if n, _ := el.Attr("name"); n == name {
			if v, ok := el.Attr("value"); ok && v != "" {
				val = v
			} else {
				val = strings.TrimSpace(el.Text())
				if val == "" {
					val = "1"
				}
			}
			return false
		}
		return true
	})
	return val
}

// ---- URL / host helpers -----------------------------------------------

func resolveURL(base, href string) (string, error) {
	bu, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	return bu.ResolveReference(r).String(), nil
}

func hostOf(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(pu.Hostname())
}

func sameHost(a, b string) bool {
	return hostOf(a) != "" && hostOf(a) == hostOf(b)
}

// ---- session storage ---------------------------------------------------

func saveSession(s *sessionState) error {
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
		return fmt.Errorf("stado_fs_write returned -1")
	}
	return nil
}

func loadSession(id string) (*sessionState, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("session_id is required (call browser_open first)")
	}
	if strings.ContainsAny(id, "/\\.") {
		return nil, fmt.Errorf("invalid session_id")
	}
	path := cacheDir + "/" + id + ".json"
	buf := make([]byte, 4<<20)
	pathBytes := []byte(path)
	n := stadoFsRead(
		uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil, fmt.Errorf("session %s not found (call browser_open first)", id)
	}
	var s sessionState
	if err := json.Unmarshal(buf[:n], &s); err != nil {
		return nil, fmt.Errorf("corrupted session file: %w", err)
	}
	return &s, nil
}

var sessionCounter uint32

func newSessionID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	suffix := time.Now().UnixNano() ^ int64(atomic.AddUint32(&sessionCounter, 1))
	suffixBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(suffixBuf, uint64(suffix))
	return hex.EncodeToString(h[:6]) + "-" + hex.EncodeToString(suffixBuf[4:])
}

// ---- byte helpers -----------------------------------------------------

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

func squashWS(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
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

// Touch one html.Node symbol so the import isn't pruned by gopls — we
// indirectly rely on x/net/html via goquery, but having the explicit
// import makes the size dependency obvious and gives us a hook for
// future direct parsing.
var _ = html.NodeType(0)

// strconv is referenced in form_index parsing edge cases; declared to
// keep the import alive across edits.
var _ = strconv.Itoa
