package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestWriteMessage_IncludesContentLength(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, map[string]any{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "Content-Length: ") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "\r\n\r\n") {
		t.Errorf("missing header/body separator: %q", out)
	}
	if !strings.Contains(out, `"hello":"world"`) {
		t.Errorf("body missing: %q", out)
	}
}

func TestWriteAndReadMessage_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := map[string]any{"id": float64(42), "method": "ping"}
	if err := WriteMessage(&buf, want); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := ReadMessage(bufio.NewReader(&buf), &got); err != nil {
		t.Fatal(err)
	}
	if got["method"] != "ping" || got["id"] != float64(42) {
		t.Errorf("round-trip mismatch: %v", got)
	}
}

func TestReadMessage_HandlesMultipleHeaders(t *testing.T) {
	payload := `{"x":1}`
	frame := "Content-Length: " + itoa(len(payload)) + "\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n" + payload
	var got map[string]int
	if err := ReadMessage(bufio.NewReader(strings.NewReader(frame)), &got); err != nil {
		t.Fatal(err)
	}
	if got["x"] != 1 {
		t.Errorf("body parse: %v", got)
	}
}

func TestReadMessage_EOFReturnsEOF(t *testing.T) {
	err := ReadMessage(bufio.NewReader(strings.NewReader("")), &struct{}{})
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReadMessage_MissingHeaderErrors(t *testing.T) {
	err := ReadMessage(bufio.NewReader(strings.NewReader("\r\n{}")), &struct{}{})
	if err == nil {
		t.Error("expected error on missing Content-Length")
	}
}

func TestWriteMessage_HonorsJSONEncoding(t *testing.T) {
	var buf bytes.Buffer
	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
	}
	if err := WriteMessage(&buf, req{JSONRPC: "2.0", ID: 7}); err != nil {
		t.Fatal(err)
	}
	i := strings.Index(buf.String(), "\r\n\r\n")
	body := buf.String()[i+4:]
	var dst req
	if err := json.Unmarshal([]byte(body), &dst); err != nil {
		t.Fatal(err)
	}
	if dst.ID != 7 {
		t.Errorf("id = %d", dst.ID)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
