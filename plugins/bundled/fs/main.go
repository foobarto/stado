//go:build wasip1

// Package main is the stado `fs` bundled plugin — read, write, edit,
// glob, grep, ls.
//
// EP-no-internal-tools Step 7: rewritten to use Tier 1 stado_fs_*
// primitives end-to-end. Pre-Step-7 four of the six tools were thin
// shims over stado_fs_tool_* delegates that called native fs.WriteTool /
// fs.EditTool / fs.GlobTool / fs.GrepTool. Those delegates are gone now;
// glob/grep walk via stado_fs_readdir, edit composes read+write,
// write uses stado_fs_write directly.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/foobarto/stado/internal/fs/hashline"
	"github.com/foobarto/stado/internal/plugins/bundled/sdk"
)

func main() {}

// ── host imports ───────────────────────────────────────────────────────────

//go:wasmimport stado stado_fs_read
func stadoFSRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_read_partial
func stadoFSReadPartial(pathPtr, pathLen, offsetHi, offsetLo, lengthHi, lengthLo, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFSWrite(pathPtr, pathLen, bufPtr, bufLen uint32) int32

//go:wasmimport stado stado_fs_readdir
func stadoFSReaddir(pathPtr, pathLen uint32, offset int32, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_stat
func stadoFSStat(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_last_error
func stadoFSLastError(bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

// ── ABI exports ────────────────────────────────────────────────────────────

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// ── fs.read ────────────────────────────────────────────────────────────────

//go:wasmexport stado_tool_read
func stadoToolRead(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
		Length int64  `json:"length"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}

	const defaultBufSize = 16 << 20
	bufCap := int32(defaultBufSize)
	buf := sdk.Alloc(bufCap)
	defer sdk.Free(buf, bufCap)

	pathBytes := []byte(req.Path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	defer sdk.Free(pathPtr, int32(len(pathBytes)))

	partial := req.Offset > 0 || req.Length > 0
	var n int32
	if partial {
		length := req.Length
		if length <= 0 {
			length = defaultBufSize
		}
		n = stadoFSReadPartial(
			uint32(pathPtr), uint32(len(pathBytes)),
			uint32(req.Offset>>32), uint32(req.Offset),
			uint32(length>>32), uint32(length),
			uint32(buf), uint32(bufCap),
		)
	} else {
		n = stadoFSRead(uint32(pathPtr), uint32(len(pathBytes)), uint32(buf), uint32(bufCap))
	}
	if n < 0 {
		if message := lastFSError(); message != "" {
			return writeError(resPtr, resCap, message)
		}
		return writeError(resPtr, resCap, "read failed")
	}
	// Full-file reads carry LINE#HASH: prefixes — byte-identical to the
	// native read tool (same hashline engine) so agents see the same anchors
	// whether fs runs native or as wasm. Byte-windowed partial reads
	// (offset/length) are an opaque-bytes surface and are returned raw.
	if partial {
		return writeResult(resPtr, resCap, sdk.Bytes(buf, n))
	}
	prefixed := hashline.Render(string(sdk.Bytes(buf, n)), 1)
	return writeResult(resPtr, resCap, []byte(prefixed))
}

// ── fs.write ───────────────────────────────────────────────────────────────

//go:wasmexport stado_tool_write
func stadoToolWrite(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	if req.Path == "" {
		return writeError(resPtr, resCap, "path required")
	}
	pathBytes := []byte(req.Path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	defer sdk.Free(pathPtr, int32(len(pathBytes)))

	contentBytes := []byte(req.Content)
	var contentPtr int32
	if len(contentBytes) > 0 {
		contentPtr = sdk.Alloc(int32(len(contentBytes)))
		sdk.Write(contentPtr, contentBytes)
		defer sdk.Free(contentPtr, int32(len(contentBytes)))
	}
	n := stadoFSWrite(
		uint32(pathPtr), uint32(len(pathBytes)),
		uint32(contentPtr), uint32(len(contentBytes)),
	)
	if n < 0 {
		msg := lastFSError()
		if msg == "" {
			msg = "write failed"
		}
		return writeError(resPtr, resCap, msg)
	}
	return writeResult(resPtr, resCap, []byte(fmt.Sprintf("wrote %d bytes to %s", n, req.Path)))
}

// ── fs.edit ────────────────────────────────────────────────────────────────

//go:wasmexport stado_tool_edit
func stadoToolEdit(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path  string `json:"path"`
		Edits []struct {
			Op          string   `json:"op"`
			Pos         string   `json:"pos"`
			End         string   `json:"end"`
			Lines       []string `json:"lines"`
			Text        string   `json:"text"`
			Replacement string   `json:"replacement"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	if req.Path == "" {
		return writeError(resPtr, resCap, "path required")
	}
	if len(req.Edits) == 0 {
		return writeError(resPtr, resCap, "edit requires at least one entry in \"edits\"")
	}

	content, err := readFileFull(req.Path)
	if err != nil {
		return writeError(resPtr, resCap, "read: "+err.Error())
	}

	// Same hashline engine as the native edit tool — anchor validation,
	// stale-anchor rejection (with fresh anchors), literal-content guard,
	// and bottom-up application are all byte-identical across native/wasm.
	edits := make([]hashline.Edit, len(req.Edits))
	for i, e := range req.Edits {
		edits[i] = hashline.Edit{Op: hashline.Op(e.Op), Pos: e.Pos, End: e.End, Lines: e.Lines, Text: e.Text, Replacement: e.Replacement}
	}
	out, err := hashline.Apply(string(content), edits)
	if err != nil {
		// Stale-anchor / validation messages are surfaced to the model so it
		// can retry with the fresh anchors carried in the error.
		return writeError(resPtr, resCap, err.Error())
	}

	if err := writeFileFull(req.Path, []byte(out)); err != nil {
		return writeError(resPtr, resCap, "write: "+err.Error())
	}
	return writeResult(resPtr, resCap, []byte(fmt.Sprintf("Applied %d edit(s) to %s", len(req.Edits), req.Path)))
}

// ── fs.glob ────────────────────────────────────────────────────────────────

const (
	maxGlobMatches = 10000
	maxGlobEntries = 50000 // hard cap on entries walked across all directories
	maxGlobDepth   = 64
)

//go:wasmexport stado_tool_glob
func stadoToolGlob(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	if req.Pattern == "" {
		return writeError(resPtr, resCap, "pattern required")
	}
	if filepath.IsAbs(req.Pattern) {
		return writeError(resPtr, resCap, "pattern must be relative to workdir")
	}
	root := req.Path
	if root == "" {
		root = "."
	}

	matches := []string{}
	walked := 0
	skipDot := !strings.HasPrefix(req.Pattern, ".")

	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		if depth > maxGlobDepth {
			return true
		}
		offset := int32(0)
		for {
			entries, err := readdirPage(dir, offset)
			if err != nil || len(entries) == 0 {
				break
			}
			for _, e := range entries {
				walked++
				if walked > maxGlobEntries {
					return false
				}
				if skipDot && strings.HasPrefix(e.Name, ".") {
					continue
				}
				rel := filepath.Join(dir, e.Name)
				if matched, _ := filepath.Match(req.Pattern, e.Name); matched {
					matches = append(matches, rel)
					if len(matches) >= maxGlobMatches {
						return false
					}
				}
				// Also try matching relative path (for patterns like "subdir/*.go")
				if rel != e.Name {
					if matched, _ := filepath.Match(req.Pattern, rel); matched {
						// Avoid double-counting when basename also matched.
						if len(matches) == 0 || matches[len(matches)-1] != rel {
							matches = append(matches, rel)
							if len(matches) >= maxGlobMatches {
								return false
							}
						}
					}
				}
				if e.Type == "dir" {
					if !walk(rel, depth+1) {
						return false
					}
				}
			}
			offset += int32(len(entries))
		}
		return true
	}
	walk(root, 0)

	out := strings.Join(matches, "\n")
	if walked > maxGlobEntries {
		out += fmt.Sprintf("\n[truncated: walked %d entries, capped at %d]", walked, maxGlobEntries)
	}
	if len(matches) >= maxGlobMatches {
		out += fmt.Sprintf("\n[truncated: %d matches, capped at %d]", len(matches), maxGlobMatches)
	}
	if out == "" {
		out = "no matches"
	}
	return writeResult(resPtr, resCap, []byte(out))
}

// ── fs.grep ────────────────────────────────────────────────────────────────

const (
	maxGrepMatches  = 5000
	maxGrepEntries  = 200000
	maxGrepFileSize = 1 << 20 // 1 MiB per file
)

//go:wasmexport stado_tool_grep
func stadoToolGrep(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	if req.Pattern == "" {
		return writeError(resPtr, resCap, "pattern required")
	}
	root := req.Path
	if root == "" {
		root = "."
	}
	re, err := regexp.Compile(req.Pattern)
	if err != nil {
		return writeError(resPtr, resCap, "invalid pattern: "+err.Error())
	}

	matches := []string{}
	walked := 0

	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		if depth > maxGlobDepth {
			return true
		}
		offset := int32(0)
		for {
			entries, err := readdirPage(dir, offset)
			if err != nil || len(entries) == 0 {
				break
			}
			for _, e := range entries {
				walked++
				if walked > maxGrepEntries {
					return false
				}
				if strings.HasPrefix(e.Name, ".") {
					continue
				}
				rel := filepath.Join(dir, e.Name)
				switch e.Type {
				case "file":
					content, err := readFileFull(rel)
					if err != nil || len(content) > maxGrepFileSize {
						continue
					}
					for i, line := range strings.Split(string(content), "\n") {
						if re.MatchString(line) {
							matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
							if len(matches) >= maxGrepMatches {
								return false
							}
						}
					}
				case "dir":
					if !walk(rel, depth+1) {
						return false
					}
				}
			}
			offset += int32(len(entries))
		}
		return true
	}
	walk(root, 0)

	out := strings.Join(matches, "\n")
	if walked > maxGrepEntries {
		out += fmt.Sprintf("\n[truncated: walked %d entries, capped at %d]", walked, maxGrepEntries)
	}
	if len(matches) >= maxGrepMatches {
		out += fmt.Sprintf("\n[truncated: %d matches, capped at %d]", len(matches), maxGrepMatches)
	}
	if out == "" {
		out = "no matches"
	}
	return writeResult(resPtr, resCap, []byte(out))
}

// ── fs.ls ──────────────────────────────────────────────────────────────────

//go:wasmexport stado_tool_ls
func stadoToolLs(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path   string `json:"path"`
		Hidden bool   `json:"hidden"`
	}
	json.Unmarshal(args, &req)
	if req.Path == "" {
		req.Path = "."
	}
	argv := []string{"/bin/ls", "-l", "--time-style=long-iso"}
	if req.Hidden {
		argv = []string{"/bin/ls", "-la", "--time-style=long-iso"}
	}
	argv = append(argv, req.Path)
	execReq, _ := json.Marshal(map[string]any{"argv": argv, "timeout_ms": 10000})
	reqPtr := sdk.Alloc(int32(len(execReq)))
	defer sdk.Free(reqPtr, int32(len(execReq)))
	sdk.Write(reqPtr, execReq)

	const cap = 1 << 20
	resBuf := sdk.Alloc(cap)
	defer sdk.Free(resBuf, cap)
	n := stadoExec(uint32(reqPtr), uint32(len(execReq)), uint32(resBuf), cap)
	if n < 0 {
		return writeError(resPtr, resCap, "exec failed")
	}
	var ex struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(sdk.Bytes(resBuf, n), &ex); err != nil {
		return writeResult(resPtr, resCap, sdk.Bytes(resBuf, n))
	}
	return writeResult(resPtr, resCap, []byte(ex.Stdout))
}

// ── fs.read_context (readctx.read) ────────────────────────────────────────

//go:wasmexport stado_tool_read_context
func stadoToolReadContext(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path  string `json:"path"`
		Line  int    `json:"line"`
		Range int    `json:"range"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}
	if req.Path == "" {
		return writeError(resPtr, resCap, "path required")
	}
	if req.Line <= 0 {
		req.Line = 1
	}
	if req.Range <= 0 {
		req.Range = 20
	}
	content, err := readFileFull(req.Path)
	if err != nil {
		return writeError(resPtr, resCap, "read: "+err.Error())
	}
	lines := strings.Split(string(content), "\n")
	start := req.Line - req.Range/2
	if start < 1 {
		start = 1
	}
	end := start + req.Range
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	width := len(fmt.Sprintf("%d", end))
	for i := start; i <= end; i++ {
		if i-1 >= len(lines) {
			break
		}
		fmt.Fprintf(&b, "%*d: %s\n", width, i, lines[i-1])
	}
	out := strings.TrimRight(b.String(), "\n")
	return writeResult(resPtr, resCap, []byte(out))
}

// ── helpers ────────────────────────────────────────────────────────────────

type dirEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode uint32 `json:"mode"`
}

// readdirPage calls stado_fs_readdir for one page starting at offset.
// Returns parsed entries; empty slice signals end-of-directory.
func readdirPage(dir string, offset int32) ([]dirEntry, error) {
	pathBytes := []byte(dir)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	defer sdk.Free(pathPtr, int32(len(pathBytes)))

	const bufCap = 256 << 10 // 256 KiB
	buf := sdk.Alloc(bufCap)
	defer sdk.Free(buf, bufCap)

	n := stadoFSReaddir(
		uint32(pathPtr), uint32(len(pathBytes)),
		offset,
		uint32(buf), uint32(bufCap),
	)
	if n < 0 {
		return nil, fmt.Errorf("stado_fs_readdir: error")
	}
	if n == 0 {
		return nil, nil
	}
	var entries []dirEntry
	if err := json.Unmarshal(sdk.Bytes(buf, n), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// readFileFull reads an entire file via stado_fs_read into a 16 MiB
// buffer. Returns content on success.
func readFileFull(path string) ([]byte, error) {
	pathBytes := []byte(path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	defer sdk.Free(pathPtr, int32(len(pathBytes)))

	const bufCap = 16 << 20
	buf := sdk.Alloc(bufCap)
	defer sdk.Free(buf, bufCap)

	n := stadoFSRead(uint32(pathPtr), uint32(len(pathBytes)), uint32(buf), uint32(bufCap))
	if n < 0 {
		return nil, errors.New("stado_fs_read failed")
	}
	out := make([]byte, n)
	copy(out, sdk.Bytes(buf, n))
	return out, nil
}

func writeFileFull(path string, content []byte) error {
	pathBytes := []byte(path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	defer sdk.Free(pathPtr, int32(len(pathBytes)))

	var contentPtr int32
	if len(content) > 0 {
		contentPtr = sdk.Alloc(int32(len(content)))
		sdk.Write(contentPtr, content)
		defer sdk.Free(contentPtr, int32(len(content)))
	}
	n := stadoFSWrite(uint32(pathPtr), uint32(len(pathBytes)), uint32(contentPtr), uint32(len(content)))
	if n < 0 {
		if msg := lastFSError(); msg != "" {
			return errors.New(msg)
		}
		return errors.New("stado_fs_write failed")
	}
	return nil
}

// lastFSError fetches the host's most recent fs error via the
// stado_fs_last_error primitive. Returns "" when there is no
// stashed error. Used after a stado_fs_* primitive returns -1
// to surface the specific cause (scope-guard violation,
// capability deny, IO failure) through the tool error envelope.
func lastFSError() string {
	const cap = 1 << 12
	buf := sdk.Alloc(cap)
	defer sdk.Free(buf, cap)
	n := stadoFSLastError(uint32(buf), uint32(cap))
	if n <= 0 {
		return ""
	}
	return string(sdk.Bytes(buf, n))
}

// writeError surfaces a tool-side error using the negative-length wire
// format the host's readToolSideError understands. Pre-Step-7 this used
// to JSON-wrap into Content with a positive return — the host then saw
// IsError=false and the model received `{"error":"..."}` as success
// content. The negative path makes PluginTool.Run set tool.Result.Error,
// which agentloop.go translates into IsError=true on the model surface.
func writeError(resPtr, resCap int32, msg string) int32 {
	b := []byte(msg)
	if int32(len(b)) > resCap {
		b = b[:resCap]
	}
	if len(b) == 0 {
		return -1
	}
	sdk.Write(resPtr, b)
	return -int32(len(b))
}

func writeResult(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
