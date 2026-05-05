// web-search — HTTP-based web search with no API key.
//
// Backends (selected by `backend` arg or auto-detected):
//   - "duckduckgo" (default): scrapes html.duckduckgo.com/html/?q=...
//     Parses result blocks via regex; URLs are unwrapped from the
//     DuckDuckGo redirect (`/l/?uddg=...`).
//   - "searxng": calls a SearXNG instance with `format=json`. Caller
//     supplies `instance_url` (e.g. https://searx.be).
//
// Tool:
//
//   web_search {query, max_results?, backend?, instance_url?}
//     → {results: [{title, url, snippet}], backend}
//
// Capabilities:
//   - net:http_request           — broad cap (any public host).
//                                  Tighten to net:http_request:html.duckduckgo.com
//                                  or your specific SearXNG host if you prefer.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

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

const httpBufCap = 4 << 20

type searchArgs struct {
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results,omitempty"`
	Backend     string `json:"backend,omitempty"`
	InstanceURL string `json:"instance_url,omitempty"`
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type searchResponse struct {
	Backend string         `json:"backend"`
	Query   string         `json:"query"`
	Results []searchResult `json:"results"`
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

//go:wasmexport stado_tool_web_search
func stadoToolWebSearch(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("web-search invoked")

	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	var a searchArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
		}
	}
	if strings.TrimSpace(a.Query) == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "query is required"})
	}
	if a.MaxResults <= 0 || a.MaxResults > 50 {
		a.MaxResults = 10
	}
	backend := strings.ToLower(strings.TrimSpace(a.Backend))
	if backend == "" {
		backend = "duckduckgo"
	}

	switch backend {
	case "duckduckgo", "ddg":
		return runDuckDuckGo(a, resultPtr, resultCap)
	case "searxng", "searx":
		if strings.TrimSpace(a.InstanceURL) == "" {
			return writeJSON(resultPtr, resultCap, errResult{
				Error: "searxng backend requires instance_url (e.g. https://searx.be)",
			})
		}
		return runSearXNG(a, resultPtr, resultCap)
	default:
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "unknown backend: " + backend + " (try duckduckgo or searxng)",
		})
	}
}

func runDuckDuckGo(a searchArgs, resultPtr, resultCap int32) int32 {
	q := url.QueryEscape(a.Query)
	target := "https://html.duckduckgo.com/html/?q=" + q

	body, status, err := httpGet(target, map[string]string{
		// Default UA matches a recent Firefox; html.duckduckgo.com
		// returns a CAPTCHA page to "anonymous" UAs.
		"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0",
		"Accept":          "text/html,application/xhtml+xml",
		"Accept-Language": "en-US,en;q=0.7",
	})
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "ddg: " + err.Error()})
	}
	if status >= 400 {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: fmt.Sprintf("ddg returned HTTP %d", status),
		})
	}

	results := parseDuckDuckGoHTML(body, a.MaxResults)
	return writeJSON(resultPtr, resultCap, searchResponse{
		Backend: "duckduckgo",
		Query:   a.Query,
		Results: results,
	})
}

func runSearXNG(a searchArgs, resultPtr, resultCap int32) int32 {
	base := strings.TrimRight(a.InstanceURL, "/")
	target := base + "/search?q=" + url.QueryEscape(a.Query) + "&format=json"

	body, status, err := httpGet(target, map[string]string{
		"User-Agent": "stado-web-search/0.1 (+https://github.com/foobarto/stado)",
		"Accept":     "application/json",
	})
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "searxng: " + err.Error()})
	}
	if status >= 400 {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: fmt.Sprintf("searxng returned HTTP %d (instance may require auth or block API access)", status),
		})
	}

	var raw struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "searxng: decode JSON: " + err.Error(),
		})
	}

	results := make([]searchResult, 0, a.MaxResults)
	for i, r := range raw.Results {
		if i >= a.MaxResults {
			break
		}
		results = append(results, searchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}

	return writeJSON(resultPtr, resultCap, searchResponse{
		Backend: "searxng",
		Query:   a.Query,
		Results: results,
	})
}

// httpGet wraps stado_http_request with the request shape used by
// every other plugin in this tree. Returns decoded body bytes,
// HTTP status, and any host-side error.
func httpGet(targetURL string, headers map[string]string) ([]byte, int, error) {
	req := hostHTTPRequest{
		Method:    "GET",
		URL:       targetURL,
		Headers:   headers,
		TimeoutMs: 15000,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	scratch := make([]byte, httpBufCap)
	n := stadoHttpRequest(
		uint32(uintptr(unsafe.Pointer(&reqBytes[0]))), uint32(len(reqBytes)),
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(httpBufCap),
	)
	if n < 0 {
		return nil, 0, fmt.Errorf("stado_http_request: %s", string(scratch[:-n]))
	}

	var resp hostHTTPResponse
	if err := json.Unmarshal(scratch[:n], &resp); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	body, err := base64.StdEncoding.DecodeString(resp.BodyB64)
	if err != nil {
		return nil, resp.Status, fmt.Errorf("decode body_b64: %w", err)
	}
	return body, resp.Status, nil
}

// ---- DuckDuckGo HTML parser ------------------------------------------

// DuckDuckGo HTML result block (post-2020 layout):
//
//	<div class="result results_links results_links_deep web-result">
//	  ...
//	  <h2 class="result__title">
//	    <a class="result__a" href="//duckduckgo.com/l/?uddg=ENCODED&...">Title</a>
//	  </h2>
//	  ...
//	  <a class="result__snippet" href="...">Snippet text</a>
//	</div>
//
// The href on result__a is a redirector — we extract `uddg` and
// URL-decode it to recover the real target.
var (
	resultBlockRE = regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>.*?<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	tagStripRE    = regexp.MustCompile(`<[^>]+>`)
	wsRE          = regexp.MustCompile(`\s+`)
)

func parseDuckDuckGoHTML(body []byte, max int) []searchResult {
	matches := resultBlockRE.FindAllSubmatch(body, -1)
	out := make([]searchResult, 0, max)
	for _, m := range matches {
		if len(out) >= max {
			break
		}
		href := string(m[1])
		title := htmlText(string(m[2]))
		snippet := htmlText(string(m[3]))
		realURL := unwrapDDGURL(href)
		if realURL == "" || title == "" {
			continue
		}
		out = append(out, searchResult{
			Title:   title,
			URL:     realURL,
			Snippet: snippet,
		})
	}
	return out
}

// unwrapDDGURL pulls the real target out of //duckduckgo.com/l/?uddg=ENC.
// Returns the input unchanged for anything that isn't a redirector.
func unwrapDDGURL(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if uddg := u.Query().Get("uddg"); uddg != "" {
		return uddg
	}
	return href
}

func htmlText(s string) string {
	stripped := tagStripRE.ReplaceAllString(s, "")
	stripped = htmlUnescape(stripped)
	return strings.TrimSpace(wsRE.ReplaceAllString(stripped, " "))
}

func htmlUnescape(s string) string {
	// Cover the four entities DDG emits in result text. We avoid a
	// full html package because the whole point of this plugin is to
	// stay small.
	r := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#x27;", "'",
		"&#39;", "'",
		"&nbsp;", " ",
	)
	return r.Replace(s)
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
