// webfetch-cached — wraps the bundled stado_http_get host import
// with a SHA-256-keyed disk cache. Same {url} input as the bundled
// webfetch tool, but a cache hit on the same URL returns immediately
// instead of re-fetching.
//
// Why this exists: Anthropic's WebFetch tool hard-codes a 15-minute
// cache TTL. For workflows that re-fetch the same writeup pages
// across many iterations (HTB box solving, vendor docs research),
// repeated fetches waste latency + quota. This plugin gives those
// workflows a persistent cache the operator controls.
//
// Authoring lineage: this is the canonical example of three things
// the v0.26.0 plugin surface enables — wrapping a bundled-tool host
// import (`stado_http_get`, gated behind `--with-tool-host`),
// declaring a workdir-rooted fs capability (`fs:read:.cache/...`,
// gated behind `--workdir`), and using `[tools].overrides` to
// transparently replace the bundled `webfetch` tool with this one.
//
// Cache layout (relative to operator's workdir / session worktree):
//
//   <workdir>/.cache/stado-webfetch/<sha256-of-url>.json
//
// File format: { "url": "<orig>", "body": "<bytes>" }
//
// Limitations:
//   - No TTL — the cache is append-only. To invalidate one URL,
//     `rm <workdir>/.cache/stado-webfetch/<sha>.json`. To wipe the
//     whole cache, `rm -rf <workdir>/.cache/stado-webfetch/`.
//   - The cache directory must already exist. Operator-side fix is
//     `mkdir -p <workdir>/.cache/stado-webfetch` once.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_fs_read
func stadoFsRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFsWrite(pathPtr, pathLen, bufPtr, bufLen uint32) int32

//go:wasmimport stado stado_http_get
func stadoHttpGet(argsPtr, argsLen, resultPtr, resultCap uint32) int32

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
func stadoFree(ptr int32, size int32) {
	pinned.Delete(uintptr(ptr))
	_ = size
}

const cacheDir = ".cache/stado-webfetch"

type fetchArgs struct {
	URL string `json:"url"`
}

type cacheEntry struct {
	URL  string `json:"url"`
	Body string `json:"body"`
}

type fetchResult struct {
	URL       string `json:"url"`
	CacheHit  bool   `json:"cache_hit"`
	Body      string `json:"body"`
	CachePath string `json:"cache_path,omitempty"`
}

type fetchError struct {
	Error string `json:"error"`
}

//go:wasmexport stado_tool_webfetch
func stadoToolWebfetch(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("webfetch-cached invoked")

	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	var a fetchArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return writeJSON(resultPtr, resultCap, fetchError{Error: "invalid JSON args: " + err.Error()})
		}
	}
	if strings.TrimSpace(a.URL) == "" {
		return writeJSON(resultPtr, resultCap, fetchError{Error: "url is required"})
	}

	sum := sha256.Sum256([]byte(a.URL))
	key := hex.EncodeToString(sum[:])
	path := cacheDir + "/" + key + ".json"

	const cacheBufCap = 1 << 22
	cacheBuf := make([]byte, cacheBufCap)
	pathBytes := []byte(path)
	n := stadoFsRead(
		uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
		uint32(uintptr(unsafe.Pointer(&cacheBuf[0]))), uint32(cacheBufCap),
	)
	if n > 0 {
		var ent cacheEntry
		if err := json.Unmarshal(cacheBuf[:n], &ent); err == nil && ent.Body != "" {
			logInfo("cache hit: " + key[:12])
			return writeJSON(resultPtr, resultCap, fetchResult{
				URL: a.URL, CacheHit: true, Body: ent.Body, CachePath: path,
			})
		}
	}

	logInfo("cache miss: " + key[:12] + " — fetching")
	const httpBufCap = 1 << 22
	httpBuf := make([]byte, httpBufCap)
	httpArgs, _ := json.Marshal(fetchArgs{URL: a.URL})
	hn := stadoHttpGet(
		uint32(uintptr(unsafe.Pointer(&httpArgs[0]))), uint32(len(httpArgs)),
		uint32(uintptr(unsafe.Pointer(&httpBuf[0]))), uint32(httpBufCap),
	)
	if hn < 0 {
		return writeJSON(resultPtr, resultCap, fetchError{
			Error: "stado_http_get returned -1; ensure plugin manifest declares net:http_get and --with-tool-host is passed (EP-0028)",
		})
	}
	body := string(httpBuf[:hn])

	ent := cacheEntry{URL: a.URL, Body: body}
	entBytes, err := json.Marshal(ent)
	if err == nil {
		writePath := []byte(path)
		stadoFsWrite(
			uint32(uintptr(unsafe.Pointer(&writePath[0]))), uint32(len(writePath)),
			uint32(uintptr(unsafe.Pointer(&entBytes[0]))), uint32(len(entBytes)),
		)
	}

	return writeJSON(resultPtr, resultCap, fetchResult{
		URL: a.URL, CacheHit: false, Body: body, CachePath: path,
	})
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
