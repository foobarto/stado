// Command fakelsp is a minimal LSP server used by the lspfind manager
// tests. It speaks just enough of the protocol for lsp.Launch's
// initialize handshake to succeed and for a definition query to return
// a deterministic location, so the tests exercise the real lsp.Client +
// real process lifecycle (reuse, crash, CloseAll) without depending on
// gopls being installed.
//
// Behaviour knobs via env:
//   - FAKELSP_PIDFILE: if set, the server writes its os.Getpid() there at
//     startup so the test can verify the process is actually killed.
//   - FAKELSP_CRASH_AFTER_INIT=1: exit(1) right after answering
//     initialize, simulating a server crash. The manager should detect
//     the dead client and relaunch on the next ClientFor.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	if pf := os.Getenv("FAKELSP_PIDFILE"); pf != "" {
		_ = os.WriteFile(pf, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	r := bufio.NewReader(os.Stdin)
	w := os.Stdout
	for {
		method, id, ok := readMessage(r)
		if !ok {
			return
		}
		switch method {
		case "initialize":
			writeResult(w, id, map[string]any{"capabilities": map[string]any{}})
			if os.Getenv("FAKELSP_CRASH_AFTER_INIT") == "1" {
				os.Exit(1)
			}
		case "shutdown":
			writeResult(w, id, nil)
		case "exit":
			os.Exit(0)
		case "textDocument/definition":
			// Deterministic single location at the document's own URI,
			// line 0 char 0. The manager test only asserts reuse / crash
			// behaviour, not the body; a non-null result is enough.
			writeResult(w, id, []map[string]any{{
				"uri": "file:///dev/null",
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 0},
				},
			}})
		default:
			// Notifications (initialized, didOpen) carry no id; ignore.
			if id != nil {
				writeResult(w, id, nil)
			}
		}
	}
}

func readMessage(r *bufio.Reader) (method string, id json.RawMessage, ok bool) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", nil, false
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if i := strings.IndexByte(line, ':'); i > 0 {
			if strings.EqualFold(strings.TrimSpace(line[:i]), "Content-Length") {
				length, _ = strconv.Atoi(strings.TrimSpace(line[i+1:]))
			}
		}
	}
	if length < 0 {
		return "", nil, false
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", nil, false
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", nil, false
	}
	return msg.Method, msg.ID, true
}

func writeResult(w io.Writer, id json.RawMessage, result any) {
	if id == nil {
		return
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": id}
	if result != nil {
		payload["result"] = result
	} else {
		payload["result"] = nil
	}
	body, _ := json.Marshal(payload)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	_, _ = w.Write(buf.Bytes())
}
