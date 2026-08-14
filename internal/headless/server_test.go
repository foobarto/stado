package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

func installHeadlessLifecycleFixture(t *testing.T, cfg *config.Config, id string) {
	t.Helper()
	dir := filepath.Join(cfg.StateDir(), "plugins", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"quality","version":"1.0.0","wasm_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capabilities":[],"tools":[],"lifecycle":{}}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessRejectsConfiguredLifecycleApplicationBeforeServing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	installHeadlessLifecycleFixture(t, cfg, "quality-1.0.0")
	cfg.Plugins.Background = []string{"quality-1.0.0"}
	applications, err := runtime.ConfiguredLifecycleApplications(cfg, nil)
	if err != nil || len(applications) != 1 {
		t.Fatalf("application classification = %#v, %v", applications, err)
	}

	err = NewServer(cfg, nil).Serve(context.Background(), strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), applications[0].CanonicalID) || !strings.Contains(err.Error(), "interactive TUI") {
		t.Fatalf("headless lifecycle diagnostic = %v", err)
	}
}

func TestHeadlessPluginRunRejectsLifecycleManifestBeforeInstantiation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	installHeadlessLifecycleFixture(t, cfg, "quality-1.0.0")
	srv := NewServer(cfg, nil)
	srv.sessions["h-1"] = &hSession{id: "h-1"}
	_, err = srv.pluginRun(context.Background(), json.RawMessage(`{"sessionId":"h-1","id":"quality-1.0.0","tool":"quality"}`))
	if err == nil || !strings.Contains(err.Error(), "ephemeral plugin.run") || !strings.Contains(err.Error(), "interactive TUI") {
		t.Fatalf("plugin.run lifecycle diagnostic = %v", err)
	}
}

// stubProvider is a minimal agent.Provider for tests that only care
// about the `Name()` seam. StreamTurn panics so we notice if a test
// accidentally hits the provider path.
type stubProvider struct{ name string }

func (s stubProvider) Name() string                     { return s.name }
func (s stubProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (s stubProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	panic("stubProvider.StreamTurn: test shouldn't invoke the provider")
}

// pipeRW wraps an io.Pipe pair into an in-memory ReadWriteCloser for tests.
type pipeRW struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeRW) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeRW) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeRW) Close() error                { p.r.Close(); return p.w.Close() }

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
	// Step 4 of EP-no-internal-tools renamed bare 'bash' to wire-form
	// 'shell__bash'.
	for _, want := range []string{`"shell__bash"`, `"fs__read"`, `"fs__grep"`, `"rg__search"`, `"lsp__definition"`} {
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
	cfg.Inference.Presets = map[string]config.InferencePreset{
		"corp-local": {Endpoint: "http://localhost:8123/v1"},
	}
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
	for _, want := range []string{"lmstudio", "litellm", "corp-local"} {
		if !contains(r.Result.Available, want) {
			t.Errorf("available missing %q: %v", want, r.Result.Available)
		}
	}
	client.Close()
}

// TestHeadless_ProvidersList_ResolvedProviderWins pins dogfood #2:
// when a provider is injected (local-fallback path in real use), the
// `current` field must report the resolved name, NOT the empty
// cfg.Defaults.Provider. Without this, scripted clients can't tell
// which backend is actually answering.
func TestHeadless_ProvidersList_ResolvedProviderWins(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	cfg := &config.Config{} // Defaults.Provider intentionally empty
	srv := NewServer(cfg, stubProvider{name: "lmstudio"})
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":9,"method":"providers.list"}`+"\n")
	reply := readLine(t, client, 2*time.Second)

	var r struct {
		Result struct {
			Current string `json:"current"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Result.Current != "lmstudio" {
		t.Errorf("current = %q, want lmstudio (resolved provider, not empty config)", r.Result.Current)
	}
	client.Close()
}

func TestHeadless_RejectsOverlappingSessionPrompt(t *testing.T) {
	srv := NewServer(&config.Config{}, &blockingPromptProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	})
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)

	res, err := srv.sessionNew(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := res.(sessionNewResult).SessionID
	first := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"first"}`)
	second := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"second"}`)

	done := make(chan error, 1)
	go func() {
		_, err := srv.sessionPrompt(context.Background(), first)
		done <- err
	}()

	prov := srv.Provider.(*blockingPromptProvider)
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first prompt did not start")
	}

	_, err = srv.sessionPrompt(context.Background(), second)
	if err == nil || !strings.Contains(err.Error(), "active operation") {
		t.Fatalf("second prompt error = %v, want active operation rejection", err)
	}

	close(prov.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first prompt returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first prompt did not complete")
	}
}

func TestHeadlessDeleteCannotRacePromptStartup(t *testing.T) {
	provider := &blockingPromptProvider{started: make(chan struct{}), release: make(chan struct{})}
	srv := NewServer(&config.Config{}, provider)
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)
	sess := &hSession{id: "h-1"}
	srv.sessions[sess.id] = sess

	// Hold the session lock so prompt startup stops after acquiring the server
	// map lock. Deletion must then wait until startup marks the session busy.
	sess.mu.Lock()
	promptDone := make(chan error, 1)
	go func() {
		_, err := srv.sessionPrompt(context.Background(), json.RawMessage(
			`{"sessionId":"h-1","prompt":"first"}`))
		promptDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for srv.mu.TryLock() {
		srv.mu.Unlock()
		if time.Now().After(deadline) {
			sess.mu.Unlock()
			t.Fatal("prompt did not acquire server lock")
		}
		time.Sleep(time.Millisecond)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := srv.sessionDelete(json.RawMessage(`{"sessionId":"h-1"}`))
		deleteDone <- err
	}()
	sess.mu.Unlock()

	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not start")
	}
	select {
	case err := <-deleteDone:
		if err == nil || !strings.Contains(err.Error(), "active operation") {
			t.Fatalf("delete error = %v, want active operation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delete remained blocked after prompt startup")
	}
	if srv.sessions["h-1"] == nil {
		t.Fatal("delete removed the starting session")
	}

	close(provider.release)
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not finish")
	}
}

type blockingPromptProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingPromptProvider) Name() string                     { return "blocking" }
func (p *blockingPromptProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *blockingPromptProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	go func() {
		defer close(ch)
		close(p.started)
		<-p.release
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: "done"}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
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
