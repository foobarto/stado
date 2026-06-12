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
//   - FAKELSP_CRASH_AFTER_INIT=1: exit(1) once the initialize handshake
//     has completed — specifically after the client's first post-initialize
//     message (e.g. the `initialized` notification) has been fully read, so
//     the exit never races the handshake write (which would surface as a
//     broken pipe on the client under -race). ClientFor still returns a
//     live client first; the manager then detects the dead client and
//     relaunches on the next ClientFor.
//   - FAKELSP_DIAG=1: on a textDocument/didOpen, push a
//     textDocument/publishDiagnostics notification echoing the opened
//     document's URI with one error diagnostic at line 0, simulating a
//     real server's post-open diagnostics push. Lets the post-edit
//     diagnostics hook be tested without gopls.
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
	crashAfterInit := os.Getenv("FAKELSP_CRASH_AFTER_INIT") == "1"
	r := bufio.NewReader(os.Stdin)
	w := os.Stdout
	initialized := false
	for {
		method, id, params, ok := readMessage(r)
		if !ok {
			return
		}
		// Crash only once the client's first post-initialize message has
		// been fully read, so the handshake write always completes before
		// the exit. Crashing right after the initialize response races the
		// client's `initialized` write → broken pipe under -race.
		if crashAfterInit && initialized {
			os.Exit(1)
		}
		switch method {
		case "initialize":
			writeResult(w, id, map[string]any{"capabilities": map[string]any{}})
			initialized = true
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
		case "textDocument/didOpen":
			// FAKELSP_DIAG=1: push diagnostics for the just-opened doc,
			// echoing its URI so the client maps them back to the file.
			if os.Getenv("FAKELSP_DIAG") == "1" {
				publishDiagnostics(w, params)
			}
		default:
			// Notifications (initialized, …) carry no id; ignore.
			if id != nil {
				writeResult(w, id, nil)
			}
		}
	}
}

// publishDiagnostics writes a textDocument/publishDiagnostics notification
// for the URI in a didOpen's params, with one error diagnostic at line 0.
func publishDiagnostics(w io.Writer, params json.RawMessage) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.TextDocument.URI == "" {
		return
	}
	writeNotification(w, "textDocument/publishDiagnostics", map[string]any{
		"uri": p.TextDocument.URI,
		"diagnostics": []map[string]any{{
			"range": map[string]any{
				"start": map[string]any{"line": 0, "character": 0},
				"end":   map[string]any{"line": 0, "character": 1},
			},
			"severity": 1, // Error
			"message":  "fakelsp: synthetic error",
			"source":   "fakelsp",
		}},
	})
}

func readMessage(r *bufio.Reader) (method string, id, params json.RawMessage, ok bool) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", nil, nil, false
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
		return "", nil, nil, false
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", nil, nil, false
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", nil, nil, false
	}
	return msg.Method, msg.ID, msg.Params, true
}

// writeNotification writes a server-initiated JSON-RPC notification (no id).
func writeNotification(w io.Writer, method string, params any) {
	payload := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	body, _ := json.Marshal(payload)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	_, _ = w.Write(buf.Bytes())
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
