package oaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/pkg/agent"
)

// Compile-time assertion: Provider satisfies agent.TokenCounter.
var _ agent.TokenCounter = (*Provider)(nil)

// sseServer returns an httptest.Server that replays the given SSE chunks
// (one per slice entry) as `data: ...\n\n` frames, terminated by `data: [DONE]`.
func sseServer(t *testing.T, method, path string, chunks []string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			t.Errorf("method = %q, want %q", r.Method, method)
		}
		if r.URL.Path != path {
			t.Errorf("path = %q, want %q", r.URL.Path, path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(s.Close)
	return s
}

func collect(t *testing.T, ch <-chan agent.Event) []agent.Event {
	t.Helper()
	var out []agent.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestStreamTurn_TextDeltas(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
	}
	srv := sseServer(t, "POST", "/v1/chat/completions", chunks)
	p, err := New(srv.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}

	ch, err := p.StreamTurn(context.Background(), agent.TurnRequest{
		Model:    "test-model",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var got strings.Builder
	var sawDone bool
	var usage *agent.Usage
	for _, e := range evs {
		if e.Kind == agent.EvTextDelta {
			got.WriteString(e.Text)
		}
		if e.Kind == agent.EvDone {
			sawDone = true
			usage = e.Usage
		}
	}
	if got.String() != "Hello world" {
		t.Errorf("text = %q, want %q", got.String(), "Hello world")
	}
	if !sawDone {
		t.Error("missing EvDone")
	}
	if usage == nil || usage.InputTokens != 7 || usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, want {7,2}", usage)
	}
}

func TestStreamTurn_ToolCall(t *testing.T) {
	idx0 := 0
	chunk1, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{
		Index: 0,
		Delta: chunkDelta{
			Role: "assistant",
			ToolCalls: []chatToolCall{{
				Index: &idx0, ID: "call_1", Type: "function",
				Function: chatFunctionCall{Name: "read_file", Arguments: `{"path":"`},
			}},
		},
	}}})
	chunk2, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{
		Delta: chunkDelta{ToolCalls: []chatToolCall{{Index: &idx0, Function: chatFunctionCall{Arguments: `foo.go"}`}}}},
	}}})
	finish := "tool_calls"
	chunk3, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{FinishReason: &finish}}})

	srv := sseServer(t, "POST", "/v1/chat/completions",
		[]string{string(chunk1), string(chunk2), string(chunk3)})
	p, _ := New(srv.URL + "/v1")

	ch, err := p.StreamTurn(context.Background(), agent.TurnRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var start, end *agent.ToolUseBlock
	var argsBuf strings.Builder
	for _, e := range evs {
		switch e.Kind {
		case agent.EvToolCallStart:
			start = e.ToolCall
		case agent.EvToolCallArgsDelta:
			argsBuf.WriteString(e.ToolArgsDelta)
		case agent.EvToolCallEnd:
			end = e.ToolCall
		}
	}
	if start == nil || start.ID != "call_1" || start.Name != "read_file" {
		t.Fatalf("start = %+v", start)
	}
	if argsBuf.String() != `{"path":"foo.go"}` {
		t.Errorf("args = %q", argsBuf.String())
	}
	if end == nil || string(end.Input) != `{"path":"foo.go"}` {
		t.Errorf("end = %+v", end)
	}
}

func TestStreamTurn_RejectsOversizedToolArgs(t *testing.T) {
	idx0 := 0
	first := strings.Repeat("a", toolinput.MaxBytes/2)
	second := strings.Repeat("b", toolinput.MaxBytes-len(first)+1)
	chunk1, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{
		Delta: chunkDelta{ToolCalls: []chatToolCall{{
			Index: &idx0, ID: "call_1", Type: "function",
			Function: chatFunctionCall{Name: "read_file", Arguments: first},
		}}},
	}}})
	chunk2, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{
		Delta: chunkDelta{ToolCalls: []chatToolCall{{
			Index:    &idx0,
			Function: chatFunctionCall{Arguments: second},
		}}},
	}}})
	srv := sseServer(t, "POST", "/v1/chat/completions", []string{string(chunk1), string(chunk2)})
	p, _ := New(srv.URL + "/v1")

	ch, err := p.StreamTurn(context.Background(), agent.TurnRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	for _, ev := range evs {
		if ev.Kind == agent.EvError && ev.Err != nil && strings.Contains(ev.Err.Error(), "tool input exceeds") {
			return
		}
	}
	t.Fatalf("missing oversized tool input error in events: %+v", evs)
}

func TestStreamTurn_ParallelToolCalls(t *testing.T) {
	i0, i1 := 0, 1
	c1, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{Delta: chunkDelta{
		ToolCalls: []chatToolCall{
			{Index: &i0, ID: "a", Function: chatFunctionCall{Name: "f1", Arguments: `{"x":1}`}},
			{Index: &i1, ID: "b", Function: chatFunctionCall{Name: "f2", Arguments: `{"y":2}`}},
		},
	}}}})
	finish := "tool_calls"
	c2, _ := json.Marshal(chatChunk{Choices: []chunkChoice{{FinishReason: &finish}}})

	srv := sseServer(t, "POST", "/v1/chat/completions", []string{string(c1), string(c2)})
	p, _ := New(srv.URL + "/v1")

	ch, _ := p.StreamTurn(context.Background(), agent.TurnRequest{Model: "m"})
	evs := collect(t, ch)

	var ends []*agent.ToolUseBlock
	for _, e := range evs {
		if e.Kind == agent.EvToolCallEnd {
			ends = append(ends, e.ToolCall)
		}
	}
	if len(ends) != 2 {
		t.Fatalf("got %d tool-call ends, want 2", len(ends))
	}
	ids := []string{ends[0].ID, ends[1].ID}
	if !((ids[0] == "a" && ids[1] == "b") || (ids[0] == "b" && ids[1] == "a")) {
		t.Errorf("tool call ids = %v, want {a,b}", ids)
	}
}

func TestStreamTurn_ReasoningContent(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"think..."}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}
	srv := sseServer(t, "POST", "/v1/chat/completions", chunks)
	p, _ := New(srv.URL + "/v1")

	ch, _ := p.StreamTurn(context.Background(), agent.TurnRequest{Model: "m"})
	evs := collect(t, ch)

	var thinking, answer string
	for _, e := range evs {
		switch e.Kind {
		case agent.EvThinkingDelta:
			thinking += e.Text
		case agent.EvTextDelta:
			answer += e.Text
		}
	}
	if thinking != "think..." || answer != "answer" {
		t.Errorf("thinking=%q answer=%q", thinking, answer)
	}
}

func TestStreamTurn_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()
	p, _ := New(srv.URL + "/v1")
	_, err := p.StreamTurn(context.Background(), agent.TurnRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want 401-branded", err)
	}
}

func TestConvertMessages_ToolResultFlow(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "read foo.go please"),
		{Role: agent.RoleAssistant, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "Sure."}},
			{ToolUse: &agent.ToolUseBlock{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"foo.go"}`)}},
		}},
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "call_1", Content: "package foo"}},
		}},
	}
	out, err := convertMessages("you are a coder", msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("got %d messages, want 4 (system+user+assistant+tool): %+v", len(out), out)
	}
	if out[0].Role != "system" {
		t.Errorf("out[0].role = %q, want system", out[0].Role)
	}
	if out[2].Role != "assistant" || len(out[2].ToolCalls) != 1 {
		t.Errorf("assistant message missing tool_calls: %+v", out[2])
	}
	if out[2].ToolCalls[0].Function.Arguments != `{"path":"foo.go"}` {
		t.Errorf("assistant tool_call args = %q", out[2].ToolCalls[0].Function.Arguments)
	}
	if out[3].Role != "tool" || out[3].ToolCallID != "call_1" {
		t.Errorf("tool message = %+v", out[3])
	}
}

func TestBuildUserMessage_Multimodal(t *testing.T) {
	blocks := []agent.Block{
		{Text: &agent.TextBlock{Text: "look at this"}},
		{Image: &agent.ImageBlock{MediaType: "image/png", Data: []byte{0x89, 0x50}}},
	}
	msg, err := buildUserMessage(blocks)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := msg.Content.([]contentPart)
	if !ok {
		t.Fatalf("content type = %T, want []contentPart", msg.Content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Errorf("parts = %+v", parts)
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("image data-url = %q", parts[1].ImageURL.URL)
	}
}

func TestNew_ValidatesEndpoint(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("New(\"\") should error")
	}
	for _, endpoint := range []string{
		"localhost:8080/v1",
		"ftp://example.com/v1",
		"http:///v1",
		"https://user:pass@example.com/v1",
	} {
		if _, err := New(endpoint); err == nil {
			t.Errorf("New(%q) should error", endpoint)
		}
	}
	// Trailing slashes are normalised away.
	p, err := New("http://localhost:8080/v1/")
	if err != nil {
		t.Fatal(err)
	}
	if p.endpoint != "http://localhost:8080/v1" {
		t.Errorf("endpoint = %q, want trailing slash stripped", p.endpoint)
	}
}

func TestProbe_ModelList(t *testing.T) {
	body := `{"data":[{"id":"qwen2.5-coder","context_length":32768}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()
	p, _ := New(srv.URL + "/v1")
	if err := p.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.Capabilities().MaxContextTokens != 32768 {
		t.Errorf("MaxContextTokens = %d, want 32768", p.Capabilities().MaxContextTokens)
	}
}

func TestProbe_RejectsOversizedModelList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxModelListResponseBytes)+1)))
	}))
	defer srv.Close()

	p, _ := New(srv.URL + "/v1")
	err := p.Probe(context.Background())
	if err == nil {
		t.Fatal("expected oversized response error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want response size rejection", err)
	}
}
