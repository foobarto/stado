package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
)

// pipeRW wraps an io.Pipe pair into an in-memory ReadWriteCloser for tests.
type pipeRW struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeRW) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeRW) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeRW) Close() error                 { p.r.Close(); return p.w.Close() }

func newPair() (client, server io.ReadWriteCloser) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return pipeRW{r: cr, w: cw}, pipeRW{r: sr, w: sw}
}

func TestHeadless_SessionNewReturnsID(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"sessionId":"h-1"`) {
		t.Errorf("reply missing sessionId: %q", reply)
	}
	if !strings.Contains(reply, `"workdir":`) {
		t.Errorf("reply missing workdir: %q", reply)
	}
	client.Close()
}

func TestHeadless_ToolsListCoversBundled(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":5,"method":"tools.list"}`+"\n")
	reply := readLine(t, client, 2*time.Second)

	// Confirm a representative sample of bundled tools appears.
	for _, want := range []string{`"bash"`, `"read"`, `"grep"`, `"ripgrep"`, `"find_definition"`} {
		if !strings.Contains(reply, want) {
			t.Errorf("tools.list missing %s:\n%s", want, reply)
		}
	}
	// Confirm class strings are present.
	if !strings.Contains(reply, `"exec"`) || !strings.Contains(reply, `"non-mutating"`) {
		t.Errorf("class strings not present:\n%s", reply)
	}
	client.Close()
}

func TestHeadless_UnknownMethodReturns32601(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":7,"method":"does.not.exist"}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"code":-32601`) {
		t.Errorf("expected -32601 method-not-found: %s", reply)
	}
	client.Close()
}

func TestHeadless_SessionPromptWithoutProvider_Errors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	// Create session first.
	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	// Prompt without provider → CodeInternalError with 'no provider'.
	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.prompt","params":{"sessionId":"h-1","prompt":"hi"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "no provider") {
		t.Errorf("reply missing 'no provider': %s", reply)
	}
	client.Close()
}

func TestHeadless_ProvidersList(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	cfg := &config.Config{}
	cfg.Defaults.Provider = "ollama"
	srv := NewServer(cfg, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":9,"method":"providers.list"}`+"\n")
	reply := readLine(t, client, 2*time.Second)

	var r struct {
		Result struct {
			Available []string `json:"available"`
			Current   string   `json:"current"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Result.Current != "ollama" {
		t.Errorf("current = %q, want ollama", r.Result.Current)
	}
	if len(r.Result.Available) < 4 {
		t.Errorf("available = %v", r.Result.Available)
	}
	client.Close()
}

func readLine(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	done := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		br := bufio.NewReader(r)
		line, err := br.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		done <- line
	}()
	select {
	case line := <-done:
		return line
	case err := <-errCh:
		t.Fatalf("read: %v", err)
	case <-time.After(timeout):
		t.Fatal("read timeout")
	}
	return ""
}
