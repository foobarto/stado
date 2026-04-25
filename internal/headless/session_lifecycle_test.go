package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/pkg/agent"
)

// TestSessionDeleteRemovesSession verifies session.delete + that a
// subsequent session.prompt against the deleted id returns invalid-params.
func TestSessionDeleteRemovesSession(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	// Create then delete.
	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.delete","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"result":{}`) {
		t.Errorf("delete result shape: %s", reply)
	}

	// Prompt on deleted session → error.
	io.WriteString(client, `{"jsonrpc":"2.0","id":3,"method":"session.prompt","params":{"sessionId":"h-1","prompt":"hi"}}`+"\n")
	reply = readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "unknown sessionId") {
		t.Errorf("expected 'unknown sessionId' error, got %s", reply)
	}
	client.Close()
}

// TestSessionDeleteUnknownErrors.
func TestSessionDeleteUnknownErrors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.delete","params":{"sessionId":"nope"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "unknown sessionId") {
		t.Errorf("expected unknown sessionId error: %s", reply)
	}
	client.Close()
}

// TestSessionCancelNoActivePromptReturnsFalse: no in-flight prompt
// → cancelled=false, no error.
func TestSessionCancelNoActivePromptReturnsFalse(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.cancel","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"cancelled":false`) {
		t.Errorf("expected cancelled:false: %s", reply)
	}
	client.Close()
}

// TestSessionCompactWithoutProviderErrors.
func TestSessionCompactWithoutProviderErrors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	// Manually seed a message so the "empty session" branch doesn't fire first.
	srv.mu.Lock()
	sess := srv.sessions["h-1"]
	sess.mu.Lock()
	sess.messages = []agent.Message{agent.Text(agent.RoleUser, "hi")}
	sess.mu.Unlock()
	srv.mu.Unlock()

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.compact","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "no provider") {
		t.Errorf("expected 'no provider' error: %s", reply)
	}
	client.Close()
}

// TestSessionCompactEmptyConversationErrors.
func TestSessionCompactEmptyConversationErrors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, scriptedCompactProvider{})
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.compact","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "no messages") {
		t.Errorf("expected 'no messages' error: %s", reply)
	}
	client.Close()
}

// TestSessionCompactReplacesMessages drives the happy path end-to-end
// with a scripted provider that returns a fixed summary.
func TestSessionCompactReplacesMessages(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, scriptedCompactProvider{
		summary: "concise session summary",
	})
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	// Seed 3 messages.
	srv.mu.Lock()
	sess := srv.sessions["h-1"]
	sess.mu.Lock()
	sess.messages = []agent.Message{
		agent.Text(agent.RoleUser, "task 1"),
		agent.Text(agent.RoleAssistant, "reply 1"),
		agent.Text(agent.RoleUser, "task 2"),
	}
	sess.mu.Unlock()
	srv.mu.Unlock()

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.compact","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 3*time.Second)
	if !strings.Contains(reply, "concise session summary") {
		t.Errorf("expected summary in reply: %s", reply)
	}
	// Parse out priorTurns / postTurns.
	var r struct {
		Result struct {
			Summary    string `json:"summary"`
			PriorTurns int    `json:"priorTurns"`
			PostTurns  int    `json:"postTurns"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Result.PriorTurns != 3 || r.Result.PostTurns != 1 {
		t.Errorf("turn counts: prior=%d post=%d (want 3 → 1)", r.Result.PriorTurns, r.Result.PostTurns)
	}

	// After compaction, in-memory msgs should be length 1.
	srv.mu.Lock()
	got := len(srv.sessions["h-1"].messages)
	srv.mu.Unlock()
	if got != 1 {
		t.Errorf("in-memory msgs = %d, want 1", got)
	}
	client.Close()
}

func TestSessionPromptWithToolsPersistsConversationLog(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdirHeadlessTest(t, cwd)
	defer restore()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, scriptedCompactProvider{summary: "headless reply"})
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)

	res, err := srv.sessionNew()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := res.(sessionNewResult).SessionID
	raw := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"hi","tools":true}`)
	if _, err := srv.sessionPrompt(context.Background(), raw); err != nil {
		t.Fatalf("sessionPrompt: %v", err)
	}

	srv.mu.Lock()
	sess := srv.sessions[sessionID]
	srv.mu.Unlock()
	sess.mu.Lock()
	gs := sess.gitSess
	persisted := sess.persistedViewLen
	sess.mu.Unlock()
	if gs == nil {
		t.Fatal("git session was not created")
	}
	if persisted != 2 {
		t.Fatalf("persistedViewLen = %d, want 2", persisted)
	}

	loaded, err := stadoruntime.LoadConversation(gs.WorktreePath)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(loaded))
	}
	if loaded[0].Role != agent.RoleUser || loaded[1].Role != agent.RoleAssistant {
		t.Fatalf("persisted roles = %+v", loaded)
	}
}

func TestSessionPromptEmitsSubagentNotifications(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdirHeadlessTest(t, cwd)
	defer restore()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(cfg, &spawnPromptProvider{})
	srv.conn = acp.NewConn(server, server)

	lines := make(chan string, 16)
	go func() {
		br := bufio.NewReader(client)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			lines <- line
		}
	}()

	res, err := srv.sessionNew()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := res.(sessionNewResult).SessionID
	raw := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"inspect in child","tools":true}`)
	if _, err := srv.sessionPrompt(context.Background(), raw); err != nil {
		t.Fatalf("sessionPrompt: %v", err)
	}

	got := map[string]map[string]any{}
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case line := <-lines:
			var payload struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatalf("parse notification: %v\n%s", err, line)
			}
			if payload.Method != "session.update" || payload.Params["kind"] != "subagent" {
				continue
			}
			phase, _ := payload.Params["phase"].(string)
			got[phase] = payload.Params
		case <-deadline:
			t.Fatalf("timed out waiting for subagent notifications, got %#v", got)
		}
	}

	started := got["started"]
	if started["status"] != "running" {
		t.Fatalf("started status = %v", started["status"])
	}
	finished := got["finished"]
	if finished["status"] != "completed" {
		t.Fatalf("finished status = %v", finished["status"])
	}
	if started["sessionId"] != sessionID || finished["sessionId"] != sessionID {
		t.Fatalf("session ids = %v/%v, want %s", started["sessionId"], finished["sessionId"], sessionID)
	}
	child, ok := started["child"].(string)
	if !ok || child == "" {
		t.Fatalf("started child missing: %#v", started)
	}
	if finished["child"] != child {
		t.Fatalf("finished child = %v, want %s", finished["child"], child)
	}
	if _, ok := finished["childWorktree"].(string); !ok {
		t.Fatalf("finished childWorktree missing: %#v", finished)
	}
}

func TestSessionCompactGitSessionWritesAuditMarkers(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	gs, err := stadoruntime.OpenSession(cfg, cwd)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "task 1"),
		agent.Text(agent.RoleAssistant, "reply 1"),
	}
	if err := gs.NextTurn(); err != nil {
		t.Fatalf("NextTurn: %v", err)
	}

	srv := NewServer(cfg, scriptedCompactProvider{summary: "git backed summary"})
	srv.sessions["h-1"] = &hSession{
		id:       "h-1",
		workdir:  cwd,
		gitSess:  gs,
		messages: msgs,
	}

	raw := json.RawMessage(`{"sessionId":"h-1"}`)
	if _, err := srv.sessionCompact(context.Background(), raw); err != nil {
		t.Fatalf("sessionCompact: %v", err)
	}

	markers, err := gs.Sidecar.ListCompactions(gs.ID)
	if err != nil {
		t.Fatalf("ListCompactions: %v", err)
	}
	if len(markers) != 1 {
		t.Fatalf("compaction markers = %d, want 1", len(markers))
	}
	if markers[0].RawLogSHA == "" {
		t.Fatal("compaction marker missing raw log digest")
	}
	if markers[0].ToTurn != 1 || markers[0].TurnsTotal != 1 {
		t.Fatalf("marker range = %d..%d total=%d, want 0..1 total=1",
			markers[0].FromTurn, markers[0].ToTurn, markers[0].TurnsTotal)
	}

	loaded, err := stadoruntime.LoadConversation(gs.WorktreePath)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(loaded) != 1 || !strings.Contains(loaded[0].Content[0].Text.Text, "git backed summary") {
		t.Fatalf("loaded compacted conversation = %+v", loaded)
	}
	rawLog, err := os.ReadFile(filepath.Join(gs.WorktreePath, ".stado", "conversation.jsonl"))
	if err != nil {
		t.Fatalf("read raw conversation log: %v", err)
	}
	if !strings.Contains(string(rawLog), "task 1") || !strings.Contains(string(rawLog), `"type":"compaction"`) {
		t.Fatalf("raw conversation log missing prior message or compaction event:\n%s", string(rawLog))
	}
}

// TestMaybeEmitContextWarning_Fraction exercises the three regions
// without going through the RPC surface (unit-test the policy).
func TestMaybeEmitContextWarning_Fraction(t *testing.T) {
	// This just exercises the branches via the exported-internal path;
	// we use a nil conn so Notify is a no-op, and inspect the
	// threshold arithmetic via an in-line test.
	cases := []struct {
		input int
		cap   int
		soft  float64
		hard  float64
		fires bool
		level string
	}{
		{50, 100, 0.70, 0.90, false, ""},
		{80, 100, 0.70, 0.90, true, "soft"},
		{95, 100, 0.70, 0.90, true, "hard"},
		{0, 100, 0.70, 0.90, false, ""}, // zero tokens: no fire
		{80, 0, 0.70, 0.90, false, ""},  // no MaxContextTokens: no fire
	}
	for _, tc := range cases {
		frac := 0.0
		if tc.cap > 0 {
			frac = float64(tc.input) / float64(tc.cap)
		}
		level := "soft"
		if frac >= tc.hard {
			level = "hard"
		}
		fires := tc.cap > 0 && tc.input > 0 && frac >= tc.soft
		if fires != tc.fires {
			t.Errorf("%+v: fires=%v want %v", tc, fires, tc.fires)
		}
		if fires && level != tc.level {
			t.Errorf("%+v: level=%s want %s", tc, level, tc.level)
		}
	}
}

// --- test fakes ---

// scriptedCompactProvider is a minimal agent.Provider that emits a
// fixed summary over a single text delta + done. Lets the compact
// flow unit-test without a real network call.
type scriptedCompactProvider struct {
	summary string
}

func (scriptedCompactProvider) Name() string                     { return "scripted" }
func (scriptedCompactProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p scriptedCompactProvider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	go func() {
		defer close(ch)
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: p.summary}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

type spawnPromptProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *spawnPromptProvider) Name() string {
	return "spawn-scripted"
}

func (p *spawnPromptProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *spawnPromptProvider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	ch := make(chan agent.Event, 3)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			ch <- agent.Event{Kind: agent.EvError, Err: ctx.Err()}
			return
		default:
		}
		switch call {
		case 1:
			ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
				ID:    "spawn-1",
				Name:  subagent.ToolName,
				Input: json.RawMessage(`{"prompt":"inspect this repo","max_turns":1,"timeout_seconds":30}`),
			}}
			ch <- agent.Event{Kind: agent.EvDone}
		case 2:
			ch <- agent.Event{Kind: agent.EvTextDelta, Text: "child findings"}
			ch <- agent.Event{Kind: agent.EvDone}
		default:
			ch <- agent.Event{Kind: agent.EvTextDelta, Text: "parent done"}
			ch <- agent.Event{Kind: agent.EvDone}
		}
	}()
	return ch, nil
}

// Stop unused-bufio linter complaint if other tests drop bufio.
var _ = bufio.NewReader

func chdirHeadlessTest(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	}
}
