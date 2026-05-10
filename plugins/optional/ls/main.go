// ls — focused single-directory listing with full file metadata.
//
// Why not just use stado_fs_tool_glob? glob returns names only, joined
// by newlines. For an LLM deciding what to do next ("is this a file or
// a dir? how big is it?") that's not enough. ls returns structured
// JSON: type, mode, size, mtime, name.
//
// Tool:
//
//   ls {path?, hidden?}
//     → {path, entries: [{name, type, size, mode, mtime}]}
//
//     hidden=true  also lists dot-files (default false).
//
// Capabilities:
//   - exec:bash       — invokes `ls -la --time-style=long-iso`
//   - fs:read:.       — required for `exec:bash` plugins to navigate
//                       the workdir under the sandbox policy
//
// Implementation note: this is a wrapper around `ls -la`, not a
// reimplementation of directory enumeration. wazero doesn't preopen
// any directory FDs for plugins, so Go's os.ReadDir traps from inside
// wasip1; the existing `stado_fs_*` host imports don't expose listing.
// `stado_fs_tool_glob` returns names only. exec:bash + parsing `ls`
// is the simplest path to structured metadata today.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_exec_bash
func stadoExecBash(argsPtr, argsLen, resultPtr, resultCap uint32) int32

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

const bashBufCap = 1 << 20

type lsArgs struct {
	Path   string `json:"path"`
	Hidden bool   `json:"hidden"`
}

type lsEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // file | dir | link | other
	Size  int64  `json:"size"`
	Mode  string `json:"mode"` // rwxr-xr-x
	MTime string `json:"mtime,omitempty"`
}

type lsResult struct {
	Path    string    `json:"path"`
	Entries []lsEntry `json:"entries"`
}

type errResult struct {
	Error string `json:"error"`
}

type bashRequest struct {
	Command string `json:"command"`
}

//go:wasmexport stado_tool_ls
func stadoToolLs(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("ls invoked")

	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	var a lsArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
		}
	}
	if strings.TrimSpace(a.Path) == "" {
		a.Path = "."
	}

	// Reject paths with single quotes outright. We single-quote the path
	// before splicing into the shell command; embedded single quotes
	// would let an attacker break out. Legitimate filenames with single
	// quotes are uncommon enough that bouncing them is acceptable for a
	// single-call enumeration tool.
	if strings.ContainsAny(a.Path, "'\n\r") {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "path contains a quote/newline; use stado_fs_tool_glob for unusual filenames",
		})
	}

	flags := "-la"
	if !a.Hidden {
		flags = "-l" // no -a: omits hidden entries; still prints . header line
	}
	cmd := fmt.Sprintf("ls %s --time-style=long-iso '%s'", flags, a.Path)
	out, err := bash(cmd)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: "bash: " + err.Error()})
	}

	entries, parseErr := parseLsLong(out)
	if parseErr != nil && len(entries) == 0 {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "parse ls output: " + parseErr.Error() + " — raw: " + truncate(out, 256),
		})
	}

	return writeJSON(resultPtr, resultCap, lsResult{
		Path:    a.Path,
		Entries: entries,
	})
}

func bash(command string) (string, error) {
	req := bashRequest{Command: command}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	scratch := make([]byte, bashBufCap)
	n := stadoExecBash(
		uint32(uintptr(unsafe.Pointer(&reqBytes[0]))), uint32(len(reqBytes)),
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(bashBufCap),
	)
	if n < 0 {
		return "", fmt.Errorf("stado_exec_bash: %s", string(scratch[:-n]))
	}
	return string(scratch[:n]), nil
}

// parseLsLong reads the output of `ls -l --time-style=long-iso` and
// turns it into structured entries.
//
// Sample input:
//
//   stdout:
//   total 12
//   drwxr-xr-x 2 user user 4096 2026-05-04 12:30 docs
//   -rw-r--r-- 1 user user  128 2026-05-04 12:31 README.md
//   lrwxrwxrwx 1 user user    7 2026-05-04 12:32 link -> README.md
//
// The bash tool wraps stdout/stderr labels around the raw output, so
// we strip a leading "stdout:\n" and trailing "stderr:\n…" if present.
func parseLsLong(raw string) ([]lsEntry, error) {
	body := raw
	body = stripLabel(body, "stdout:\n")
	// Drop everything from "stderr:" onward — keep stderr out of parsing.
	if idx := strings.Index(body, "\nstderr:\n"); idx >= 0 {
		body = body[:idx]
	}

	var entries []lsEntry
	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if i == 0 && strings.HasPrefix(line, "total ") {
			continue
		}
		entry, ok := parseLsLine(line)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries parsed")
	}
	return entries, nil
}

// parseLsLine parses one row of `ls -l --time-style=long-iso`:
//
//   drwxr-xr-x 2 user user 4096 2026-05-04 12:30 some-name
//   ^perm     ^nlink ^owner ^group ^size ^date  ^time ^name (rest)
//
// On a symlink the name part is "<name> -> <target>" — we keep only the
// name. Any line that doesn't match the layout is skipped silently.
func parseLsLine(line string) (lsEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return lsEntry{}, false
	}
	perm := fields[0]
	if len(perm) < 10 {
		return lsEntry{}, false
	}
	size, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return lsEntry{}, false
	}
	date := fields[5]
	clock := fields[6]
	mtime := date + " " + clock

	// Name = everything after the time field. Re-join with single space;
	// trailing chunks reglue space-bearing names well enough for typical
	// inputs and `ls` doesn't promise quoting otherwise.
	nameParts := fields[7:]
	name := strings.Join(nameParts, " ")
	if arrow := strings.Index(name, " -> "); arrow >= 0 {
		name = name[:arrow]
	}

	kind := "other"
	switch perm[0] {
	case '-':
		kind = "file"
	case 'd':
		kind = "dir"
	case 'l':
		kind = "link"
	case 'b', 'c', 'p', 's':
		kind = "special"
	}

	return lsEntry{
		Name:  name,
		Type:  kind,
		Size:  size,
		Mode:  perm[1:],
		MTime: mtime,
	}, true
}

func stripLabel(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
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
