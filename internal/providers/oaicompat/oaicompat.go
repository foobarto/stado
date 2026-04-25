// Package oaicompat is a hand-rolled OpenAI-compatible HTTP client.
//
// Covers llama.cpp (llama-server), vLLM, ollama, LM Studio, LiteLLM,
// OpenRouter, Groq, Cerebras, xAI, DeepSeek, Mistral — anything that
// speaks /v1/chat/completions.
// No third-party SDK; stdlib HTTP + SSE parsing (~300 LOC).
package oaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/providers/tokenize"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/pkg/agent"
)

// Provider is an OAI-compat HTTP provider. Zero value is not usable; use New.
type Provider struct {
	endpoint   string // e.g. "http://localhost:11434/v1"
	apiKey     string
	name       string
	httpClient *http.Client
	caps       agent.Capabilities
}

type Option func(*Provider)

func WithAPIKey(k string) Option                   { return func(p *Provider) { p.apiKey = k } }
func WithHTTPClient(c *http.Client) Option         { return func(p *Provider) { p.httpClient = c } }
func WithName(n string) Option                     { return func(p *Provider) { p.name = n } }
func WithCapabilities(c agent.Capabilities) Option { return func(p *Provider) { p.caps = c } }

// New returns a provider pointing at endpoint. Endpoint should include the
// "/v1" suffix for most servers (llama.cpp, vLLM, ollama OpenAI mode).
func New(endpoint string, opts ...Option) (*Provider, error) {
	if endpoint == "" {
		return nil, errors.New("oaicompat: endpoint required")
	}
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("oaicompat: invalid endpoint %q: %w", endpoint, err)
	}
	p := &Provider{
		endpoint:   strings.TrimRight(endpoint, "/"),
		name:       "oaicompat",
		httpClient: &http.Client{Timeout: 0}, // streams; no overall deadline
		caps: agent.Capabilities{
			MaxParallelToolCalls: 1, // conservative default; probe can raise
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *Provider) Name() string                     { return p.name }
func (p *Provider) Capabilities() agent.Capabilities { return p.caps }

// Probe hits /v1/models and updates capabilities where the server exposes
// them. Best-effort: /v1/models doesn't expose tool-calling support on most
// backends, so tool support is learned on first real call.
func (p *Provider) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.endpoint+"/models", nil)
	if err != nil {
		return err
	}
	p.setAuth(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return friendlyError(err, p.endpoint)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("oaicompat: authentication failed at %s — check API key", p.endpoint)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("oaicompat: %s/models not found — is this really an OpenAI-compatible server?", p.endpoint)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("oaicompat: probe HTTP %d: %s", resp.StatusCode, string(b))
	}
	var list modelsList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return fmt.Errorf("oaicompat: decode models list: %w", err)
	}
	for _, m := range list.Data {
		if m.ContextLength > p.caps.MaxContextTokens {
			p.caps.MaxContextTokens = m.ContextLength
		}
	}
	return nil
}

// CountTokens uses tiktoken as the default tokenizer for all OAI-compat
// servers. Servers that bundle a more accurate endpoint-server-side
// counter (e.g. llama.cpp's `/tokenize`) could override this in future.
// See DESIGN §"Token accounting".
func (p *Provider) CountTokens(_ context.Context, req agent.TurnRequest) (int, error) {
	return tokenize.CountOAI(req.Model, req)
}

// StreamTurn posts to /chat/completions (streaming) and translates SSE chunks
// to agent.Event values on the returned channel. The channel closes when the
// turn finishes or errors.
func (p *Provider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	body, err := buildRequest(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	p.setAuth(httpReq)

	ctx, span := otel.Tracer(telemetry.TracerName).Start(ctx, telemetry.SpanProviderStream,
		trace.WithAttributes(
			attribute.String("provider.name", p.name),
			attribute.String("provider.model", req.Model),
			attribute.Int("provider.messages", len(req.Messages)),
			attribute.Int("provider.tools", len(req.Tools)),
		),
	)
	_ = ctx

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.End()
		return nil, friendlyError(err, p.endpoint)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		serr := httpStatusError(resp.StatusCode, b, p.endpoint)
		span.RecordError(serr)
		span.SetStatus(codes.Error, serr.Error())
		span.End()
		return nil, serr
	}

	events := make(chan agent.Event, 16)
	go parseSSE(resp.Body, events, span)
	return events, nil
}

func (p *Provider) setAuth(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

// buildRequest translates an agent.TurnRequest to the OAI chat-completions JSON.
func buildRequest(req agent.TurnRequest) ([]byte, error) {
	msgs, err := convertMessages(req.System, req.Messages)
	if err != nil {
		return nil, err
	}
	var tools []chatTool
	for _, t := range req.Tools {
		tools = append(tools, chatTool{
			Type: "function",
			Function: chatFunctionSpec{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}
	payload := chatRequest{
		Model:       req.Model,
		Messages:    msgs,
		Stream:      true,
		StreamOpts:  &streamOptions{IncludeUsage: true},
		Tools:       tools,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	return json.Marshal(payload)
}

// convertMessages flattens agent Block lists into OAI-shaped messages.
// Assistant messages with multiple tool_use blocks collapse into one message
// with tool_calls[]. Tool results become role="tool" messages. Images become
// user-role messages with multimodal content parts.
func convertMessages(system string, msgs []agent.Message) ([]chatMessage, error) {
	var out []chatMessage
	if system != "" {
		out = append(out, chatMessage{Role: "system", Content: system})
	}

	for _, m := range msgs {
		switch m.Role {
		case agent.RoleUser:
			user, err := buildUserMessage(m.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, user)

		case agent.RoleAssistant:
			asst := chatMessage{Role: "assistant"}
			var textBuf strings.Builder
			for _, b := range m.Content {
				switch {
				case b.Text != nil:
					textBuf.WriteString(b.Text.Text)
				case b.ToolUse != nil:
					asst.ToolCalls = append(asst.ToolCalls, chatToolCall{
						ID:   b.ToolUse.ID,
						Type: "function",
						Function: chatFunctionCall{
							Name:      b.ToolUse.Name,
							Arguments: string(b.ToolUse.Input),
						},
					})
				}
				// Thinking blocks: most OAI-compat servers don't accept them on
				// replay; drop silently. Providers that want round-trip
				// (anthropic) handle it in their own implementation.
			}
			if textBuf.Len() > 0 {
				asst.Content = textBuf.String()
			}
			out = append(out, asst)

		case agent.RoleTool:
			// Each tool_result becomes its own tool message.
			for _, b := range m.Content {
				if b.ToolResult == nil {
					continue
				}
				out = append(out, chatMessage{
					Role:       "tool",
					ToolCallID: b.ToolResult.ToolUseID,
					Content:    b.ToolResult.Content,
				})
			}
		}
	}
	return out, nil
}

func buildUserMessage(blocks []agent.Block) (chatMessage, error) {
	// Fast path: single text block → string content.
	if len(blocks) == 1 && blocks[0].Text != nil {
		return chatMessage{Role: "user", Content: blocks[0].Text.Text}, nil
	}
	var parts []contentPart
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			parts = append(parts, contentPart{Type: "text", Text: b.Text.Text})
		case b.Image != nil:
			dataURL := "data:" + b.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(b.Image.Data)
			parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURLPart{URL: dataURL}})
		}
	}
	return chatMessage{Role: "user", Content: parts}, nil
}

// parseSSE reads an SSE stream of chat-completions chunks from r and emits
// agent events on ch. Closes ch when the stream ends. Ends span on return.
func parseSSE(r io.ReadCloser, ch chan<- agent.Event, span trace.Span) {
	defer close(ch)
	defer r.Close()
	defer span.End()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// State for accumulating tool calls across streaming chunks. Parallel
	// calls are distinguished by chunk-level `index`.
	type pending struct {
		id   string
		name string
		args strings.Builder
	}
	calls := map[int]*pending{}
	order := []int{}

	var lastUsage *agent.Usage

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			// End-of-stream sentinel. Some OAI-compat servers (notably
			// lmstudio) emit `data: [DONE]` and then keep the HTTP
			// connection open waiting for the client to close — a
			// plain `continue` here made scanner.Scan() block forever
			// and the TUI sat in state=streaming after the final token
			// had already arrived. Treat [DONE] as EOF: flush any
			// in-progress tool calls and return like a normal finish.
			for _, idx := range order {
				p := calls[idx]
				ch <- agent.Event{
					Kind: agent.EvToolCallEnd,
					ToolCall: &agent.ToolUseBlock{
						ID:    p.id,
						Name:  p.name,
						Input: json.RawMessage(p.args.String()),
					},
				}
			}
			recordUsageSpan(span, lastUsage)
			ch <- agent.Event{Kind: agent.EvDone, Usage: lastUsage}
			return
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("oaicompat: decode chunk: %w", err)}
			return
		}

		if chunk.Usage != nil {
			lastUsage = &agent.Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.ReasoningContent != "" {
				ch <- agent.Event{Kind: agent.EvThinkingDelta, Text: choice.Delta.ReasoningContent}
			}
			if choice.Delta.Content != "" {
				ch <- agent.Event{Kind: agent.EvTextDelta, Text: choice.Delta.Content}
			}
			for _, tc := range choice.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				p, exists := calls[idx]
				if !exists {
					p = &pending{}
					calls[idx] = p
					order = append(order, idx)
				}
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				// First chunk for a call carries id+name; emit Start once ready.
				if !exists && p.id != "" && p.name != "" {
					ch <- agent.Event{
						Kind: agent.EvToolCallStart,
						ToolCall: &agent.ToolUseBlock{
							ID:   p.id,
							Name: p.name,
						},
					}
				}
				if tc.Function.Arguments != "" {
					p.args.WriteString(tc.Function.Arguments)
					ch <- agent.Event{
						Kind:          agent.EvToolCallArgsDelta,
						ToolArgsDelta: tc.Function.Arguments,
					}
				}
			}
			if choice.FinishReason != nil {
				for _, idx := range order {
					p := calls[idx]
					ch <- agent.Event{
						Kind: agent.EvToolCallEnd,
						ToolCall: &agent.ToolUseBlock{
							ID:    p.id,
							Name:  p.name,
							Input: json.RawMessage(p.args.String()),
						},
					}
				}
				recordUsageSpan(span, lastUsage)
				ch <- agent.Event{Kind: agent.EvDone, Usage: lastUsage}
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		ch <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("oaicompat: stream read: %w", err)}
		return
	}
	// Stream ended without explicit finish_reason (some servers do this).
	for _, idx := range order {
		p := calls[idx]
		ch <- agent.Event{
			Kind: agent.EvToolCallEnd,
			ToolCall: &agent.ToolUseBlock{
				ID:    p.id,
				Name:  p.name,
				Input: json.RawMessage(p.args.String()),
			},
		}
	}
	recordUsageSpan(span, lastUsage)
	ch <- agent.Event{Kind: agent.EvDone, Usage: lastUsage}
}

func recordUsageSpan(span trace.Span, u *agent.Usage) {
	if u == nil {
		return
	}
	span.SetAttributes(
		attribute.Int("provider.input_tokens", u.InputTokens),
		attribute.Int("provider.output_tokens", u.OutputTokens),
	)
}

// friendlyError unwraps net errors into human-readable messages with the
// endpoint called out, so users see "connection refused at localhost:11434 —
// is ollama running?" rather than a raw dial error.
func friendlyError(err error, endpoint string) error {
	if err == nil {
		return nil
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		host := endpoint
		if u, perr := url.Parse(endpoint); perr == nil {
			host = u.Host
		}
		switch {
		case strings.Contains(opErr.Error(), "connection refused"):
			return fmt.Errorf("oaicompat: connection refused at %s — is the server running? (ollama: `ollama serve`; llama.cpp: `llama-server`; vLLM: `vllm serve <model>`; LM Studio: load a model in the app and enable the local server on port 1234)", host)
		case errors.Is(opErr.Err, context.DeadlineExceeded):
			return fmt.Errorf("oaicompat: timeout connecting to %s", host)
		}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("oaicompat: DNS lookup failed for %s: %w", endpoint, err)
	}
	return fmt.Errorf("oaicompat: %w", err)
}

func httpStatusError(code int, body []byte, endpoint string) error {
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("oaicompat: 401 at %s — authentication failed, check API key", endpoint)
	case http.StatusNotFound:
		return fmt.Errorf("oaicompat: 404 at %s — endpoint path wrong or model not loaded", endpoint)
	case http.StatusTooManyRequests:
		return fmt.Errorf("oaicompat: 429 at %s — rate limited, retry after backoff", endpoint)
	case http.StatusBadRequest:
		return fmt.Errorf("oaicompat: 400 at %s: %s", endpoint, snippet(body))
	}
	return fmt.Errorf("oaicompat: HTTP %d at %s: %s", code, endpoint, snippet(body))
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
