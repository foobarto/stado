package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Client speaks LSP over stdio to a language server child process.
type Client struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	wMu      sync.Mutex
	rMu      sync.Mutex
	id       atomic.Int64
	done     chan struct{}
	pending  sync.Map // id → chan rawResponse
	initRoot string
}

type rawMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp %d: %s", e.Code, e.Message) }

type rawResponse struct {
	result json.RawMessage
	err    *rpcError
}

// Launch starts the named language-server binary (gopls, rust-analyzer,
// pyright, …) rooted at projectRoot and performs the LSP initialize
// handshake. Caller owns Close().
func Launch(ctx context.Context, server, projectRoot string) (*Client, error) {
	bin, err := exec.LookPath(server)
	if err != nil {
		return nil, fmt.Errorf("lsp: %s not on PATH — install %s", server, installHint(server))
	}
	cmd := exec.CommandContext(ctx, bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReaderSize(stdout, 64*1024),
		done:     make(chan struct{}),
		initRoot: projectRoot,
	}
	go c.readLoop()

	if err := c.initialize(ctx, projectRoot); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	// Best-effort shutdown/exit handshake before killing.
	ctx, cancel := contextWithTimeout(1) // 1s
	defer cancel()
	_, _ = c.call(ctx, "shutdown", nil)
	_ = c.notify("exit", nil)
	close(c.done)
	_ = c.stdin.Close()
	_ = c.cmd.Process.Kill()
	return c.cmd.Wait()
}

// readLoop pumps messages from the server into the pending-response map (for
// responses) or a no-op (for server-initiated notifications we don't care
// about in v1).
func (c *Client) readLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}
		c.rMu.Lock()
		var msg rawMessage
		err := ReadMessage(c.stdout, &msg)
		c.rMu.Unlock()
		if err != nil {
			// On EOF, signal pending callers so they unblock.
			c.pending.Range(func(k, v any) bool {
				ch := v.(chan rawResponse)
				select {
				case ch <- rawResponse{err: &rpcError{Code: -32001, Message: "server disconnected"}}:
				default:
				}
				return true
			})
			return
		}
		if len(msg.ID) == 0 {
			// Server notification — ignore in v1.
			continue
		}
		var idNum int64
		if err := json.Unmarshal(msg.ID, &idNum); err != nil {
			continue
		}
		if ch, ok := c.pending.LoadAndDelete(idNum); ok {
			ch.(chan rawResponse) <- rawResponse{result: msg.Result, err: msg.Error}
		}
	}
}

// call sends a request and waits for the matching response.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.id.Add(1)
	idJSON, _ := json.Marshal(id)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ch := make(chan rawResponse, 1)
	c.pending.Store(id, ch)

	c.wMu.Lock()
	err = WriteMessage(c.stdin, rawMessage{
		JSONRPC: "2.0",
		ID:      idJSON,
		Method:  method,
		Params:  paramsJSON,
	})
	c.wMu.Unlock()
	if err != nil {
		c.pending.Delete(id)
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.err != nil {
			return nil, resp.err
		}
		return resp.result, nil
	case <-ctx.Done():
		c.pending.Delete(id)
		return nil, ctx.Err()
	}
}

// notify sends a notification (no ID, no response).
func (c *Client) notify(method string, params any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.wMu.Lock()
	defer c.wMu.Unlock()
	return WriteMessage(c.stdin, rawMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	})
}

// --- LSP message surface (minimal) ---

type initializeParams struct {
	ProcessID    int              `json:"processId"`
	RootURI      string           `json:"rootUri"`
	Capabilities map[string]any   `json:"capabilities"`
}

func (c *Client) initialize(ctx context.Context, root string) error {
	absRoot, _ := filepath.Abs(root)
	rootURI := pathToURI(absRoot)
	_, err := c.call(ctx, "initialize", initializeParams{
		ProcessID: os.Getpid(),
		RootURI:   rootURI,
		Capabilities: map[string]any{
			"textDocument": map[string]any{
				"definition": map[string]any{"dynamicRegistration": false},
				"hover":      map[string]any{"dynamicRegistration": false},
			},
		},
	})
	if err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

// DidOpen announces a text document to the server. Required before
// textDocument/definition returns useful results.
func (c *Client) DidOpen(path, languageID, text string) error {
	absPath, _ := filepath.Abs(path)
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        pathToURI(absPath),
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// Position is the LSP 0-indexed line + UTF-16 code-unit character offset.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Location is an LSP result — file + range.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Definition queries textDocument/definition. Returns 0 or more locations;
// gopls typically returns exactly one for a Go identifier.
func (c *Client) Definition(ctx context.Context, path string, pos Position) ([]Location, error) {
	absPath, _ := filepath.Abs(path)
	raw, err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(absPath)},
		"position":     pos,
	})
	if err != nil {
		return nil, err
	}
	// Response can be Location | Location[] | LocationLink[]; we handle the
	// two common shapes, treat LocationLink[] as best-effort Location[].
	if len(raw) == 0 || bytesEqual(raw, "null") {
		return nil, nil
	}
	if raw[0] == '[' {
		var locs []Location
		if err := json.Unmarshal(raw, &locs); err == nil && len(locs) > 0 {
			return locs, nil
		}
		var links []struct {
			TargetURI   string `json:"targetUri"`
			TargetRange struct {
				Start Position `json:"start"`
				End   Position `json:"end"`
			} `json:"targetRange"`
		}
		if err := json.Unmarshal(raw, &links); err == nil {
			out := make([]Location, len(links))
			for i, l := range links {
				out[i] = Location{URI: l.TargetURI}
				out[i].Range.Start = l.TargetRange.Start
				out[i].Range.End = l.TargetRange.End
			}
			return out, nil
		}
	}
	var loc Location
	if err := json.Unmarshal(raw, &loc); err == nil && loc.URI != "" {
		return []Location{loc}, nil
	}
	return nil, errors.New("lsp: unrecognised definition response shape")
}

// References queries textDocument/references; returns every location
// referencing the symbol at the given position (including declaration).
func (c *Client) References(ctx context.Context, path string, pos Position, includeDeclaration bool) ([]Location, error) {
	absPath, _ := filepath.Abs(path)
	raw, err := c.call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(absPath)},
		"position":     pos,
		"context":      map[string]any{"includeDeclaration": includeDeclaration},
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || bytesEqual(raw, "null") {
		return nil, nil
	}
	var locs []Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// DocumentSymbol is a single symbol in a file's outline.
type DocumentSymbol struct {
	Name     string           `json:"name"`
	Detail   string           `json:"detail,omitempty"`
	Kind     int              `json:"kind"`
	Range    Range            `json:"range"`
	Selection Range           `json:"selectionRange"`
	Children []DocumentSymbol `json:"children,omitempty"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// DocumentSymbols queries textDocument/documentSymbol; returns the file's
// top-level symbol outline. Servers may return either DocumentSymbol[] (the
// hierarchical form, preferred) or SymbolInformation[] (flat, legacy).
func (c *Client) DocumentSymbols(ctx context.Context, path string) ([]DocumentSymbol, error) {
	absPath, _ := filepath.Abs(path)
	raw, err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(absPath)},
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || bytesEqual(raw, "null") {
		return nil, nil
	}
	var syms []DocumentSymbol
	if err := json.Unmarshal(raw, &syms); err == nil && len(syms) > 0 && syms[0].Name != "" {
		return syms, nil
	}
	// Legacy SymbolInformation[] — flatten into a Name-only outline.
	var legacy []struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Location Location `json:"location"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	out := make([]DocumentSymbol, len(legacy))
	for i, l := range legacy {
		out[i] = DocumentSymbol{
			Name:      l.Name,
			Kind:      l.Kind,
			Range:     l.Location.Range,
			Selection: l.Location.Range,
		}
	}
	return out, nil
}

// Hover queries textDocument/hover; returns formatted markdown/plain text or "".
func (c *Client) Hover(ctx context.Context, path string, pos Position) (string, error) {
	absPath, _ := filepath.Abs(path)
	raw, err := c.call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(absPath)},
		"position":     pos,
	})
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || bytesEqual(raw, "null") {
		return "", nil
	}
	var resp struct {
		Contents any `json:"contents"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", err
	}
	switch v := resp.Contents.(type) {
	case string:
		return v, nil
	case map[string]any:
		if s, ok := v["value"].(string); ok {
			return s, nil
		}
	}
	return "", nil
}

// URIToPath converts an LSP file:// URI back to a filesystem path.
func URIToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	if u.Scheme != "file" {
		return uri
	}
	return u.Path
}

func pathToURI(path string) string {
	// LSP file URIs use triple-slash absolute-path form: file:///home/foo
	return "file://" + path
}

func bytesEqual(a []byte, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// installHint returns install instructions per known server.
func installHint(name string) string {
	switch name {
	case "gopls":
		return "`go install golang.org/x/tools/gopls@latest`"
	case "rust-analyzer":
		return "`rustup component add rust-analyzer`"
	case "pyright":
		return "`npm i -g pyright`"
	}
	return "it via your package manager"
}

// contextWithTimeout is a small indirection so tests can stub deadlines
// without dragging `time` into the calling file.
var contextWithTimeout = defaultContextWithTimeout
