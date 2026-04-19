// Package lsp is a minimal pure-Go LSP client for stado's Phase 4.3 tool
// runtime.
//
// v1 scope:
//   - stdio transport with Content-Length framing
//   - gopls process launcher (other language servers land in a follow-up)
//   - textDocument/definition + hover + documentSymbol
//
// No external LSP library — go.lsp.dev is an option but its API surface is
// larger than we need. Hand-rolled is ~300 LOC and keeps the import
// footprint small.
package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WriteMessage serialises a JSON-RPC message with LSP's Content-Length frame.
func WriteMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("lsp: marshal: %w", err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	_, err = w.Write(buf.Bytes())
	return err
}

// ReadMessage reads one Content-Length-framed JSON message and unmarshals
// it into dst. Returns io.EOF cleanly when the peer closes.
func ReadMessage(r *bufio.Reader, dst any) error {
	length, err := readFrameHeader(r)
	if err != nil {
		return err
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return fmt.Errorf("lsp: read body: %w", err)
	}
	return json.Unmarshal(body, dst)
}

// readFrameHeader reads the LSP header block (Content-Length + optional
// Content-Type) and returns the body length. Skips empty lines.
func readFrameHeader(r *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if i := strings.IndexByte(line, ':'); i > 0 {
			key := strings.TrimSpace(line[:i])
			val := strings.TrimSpace(line[i+1:])
			if strings.EqualFold(key, "Content-Length") {
				n, err := strconv.Atoi(val)
				if err != nil {
					return 0, fmt.Errorf("lsp: bad content-length %q: %w", val, err)
				}
				length = n
			}
		}
	}
	if length < 0 {
		return 0, fmt.Errorf("lsp: missing Content-Length header")
	}
	return length, nil
}
