// Package openai is the direct openai-go implementation of pkg/agent.Provider.
//
// Wraps Chat Completions with parallel tool calls and JSON-schema tools.
// reasoning_content from OpenAI's reasoning models is exposed via the Responses
// API rather than Chat Completions; use the oaicompat provider for DeepSeek /
// third-party servers that emit reasoning_content on chat completions.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/providers/tokenize"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/pkg/agent"
)

type Provider struct {
	client sdk.Client
	name   string
}

func New(apiKey, baseURL string) (*Provider, error) {
	if apiKey == "" {
		apiKey = config.ResolveProviderAPIKey("openai")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("openai: %s not set", config.ProviderAPIKeyEnv("openai"))
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Provider{client: sdk.NewClient(opts...), name: "openai"}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsPromptCache:  true,
		SupportsThinking:     false, // via Responses API only; not in this provider
		MaxParallelToolCalls: 8,
		SupportsVision:       true,
		MaxContextTokens:     128_000,
	}
}

// CountTokens uses tiktoken (offline BPE loader) to return the prompt-side
// token count for req under req.Model's encoding. Zero network calls.
// See DESIGN §"Token accounting".
func (p *Provider) CountTokens(_ context.Context, req agent.TurnRequest) (int, error) {
	return tokenize.CountOAI(req.Model, req)
}

func (p *Provider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	params, err := buildParams(req)
	if err != nil {
		return nil, err
	}
	ctx, span := otel.Tracer(telemetry.TracerName).Start(ctx, telemetry.SpanProviderStream,
		trace.WithAttributes(
			attribute.String("provider.name", p.name),
			attribute.String("provider.model", req.Model),
			attribute.Int("provider.messages", len(req.Messages)),
			attribute.Int("provider.tools", len(req.Tools)),
		),
	)
	s := p.client.Chat.Completions.NewStreaming(ctx, params)
	ch := make(chan agent.Event, 16)
	go streamChunks(s, ch, span)
	return ch, nil
}

type chunkStream = ssestream.Stream[sdk.ChatCompletionChunk]

func streamChunks(s *chunkStream, ch chan<- agent.Event, span trace.Span) {
	defer close(ch)
	defer span.End()

	type pending struct {
		id   string
		name string
		args strings.Builder
	}
	// Parallel tool calls: keyed by delta.index.
	calls := map[int64]*pending{}
	var order []int64
	var usage *agent.Usage

	for s.Next() {
		chunk := s.Current()
		if chunk.Usage.TotalTokens > 0 {
			usage = &agent.Usage{
				InputTokens:  int(chunk.Usage.PromptTokens),
				OutputTokens: int(chunk.Usage.CompletionTokens),
			}
		}
		for _, choice := range chunk.Choices {
			d := choice.Delta
			if d.Content != "" {
				ch <- agent.Event{Kind: agent.EvTextDelta, Text: d.Content}
			}
			for _, tc := range d.ToolCalls {
				p, exists := calls[tc.Index]
				if !exists {
					p = &pending{}
					calls[tc.Index] = p
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				if !exists && p.id != "" && p.name != "" {
					ch <- agent.Event{
						Kind:     agent.EvToolCallStart,
						ToolCall: &agent.ToolUseBlock{ID: p.id, Name: p.name},
					}
				}
				if tc.Function.Arguments != "" {
					p.args.WriteString(tc.Function.Arguments)
					ch <- agent.Event{Kind: agent.EvToolCallArgsDelta, ToolArgsDelta: tc.Function.Arguments}
				}
			}
			if choice.FinishReason != "" {
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
				recordUsageAttrs(span, usage)
				ch <- agent.Event{Kind: agent.EvDone, Usage: usage}
				return
			}
		}
	}

	if err := s.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		ch <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("openai: %w", err)}
		return
	}
	// Stream ended without a finish_reason (shouldn't normally happen).
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
	recordUsageAttrs(span, usage)
	ch <- agent.Event{Kind: agent.EvDone, Usage: usage}
}

func recordUsageAttrs(span trace.Span, u *agent.Usage) {
	if u == nil {
		return
	}
	span.SetAttributes(
		attribute.Int("provider.input_tokens", u.InputTokens),
		attribute.Int("provider.output_tokens", u.OutputTokens),
	)
}

func buildParams(req agent.TurnRequest) (sdk.ChatCompletionNewParams, error) {
	messages, err := convertMessages(req.System, req.Messages)
	if err != nil {
		return sdk.ChatCompletionNewParams{}, err
	}
	params := sdk.ChatCompletionNewParams{
		Model:    sdk.ChatModel(req.Model),
		Messages: messages,
		StreamOptions: sdk.ChatCompletionStreamOptionsParam{
			IncludeUsage: sdk.Bool(true),
		},
	}
	if req.Temperature != nil {
		params.Temperature = sdk.Float(*req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = sdk.Int(int64(req.MaxTokens))
	}
	if len(req.Tools) > 0 {
		tools := make([]sdk.ChatCompletionToolParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			var schema map[string]any
			if err := json.Unmarshal(t.Schema, &schema); err != nil {
				return params, fmt.Errorf("openai: tool %q schema: %w", t.Name, err)
			}
			tools = append(tools, sdk.ChatCompletionToolParam{
				Function: sdk.FunctionDefinitionParam{
					Name:        t.Name,
					Description: sdk.String(t.Description),
					Parameters:  sdk.FunctionParameters(schema),
				},
			})
		}
		params.Tools = tools
		params.ParallelToolCalls = sdk.Bool(true)
	}
	return params, nil
}

func convertMessages(system string, msgs []agent.Message) ([]sdk.ChatCompletionMessageParamUnion, error) {
	var out []sdk.ChatCompletionMessageParamUnion
	if system != "" {
		out = append(out, sdk.SystemMessage(system))
	}
	for _, m := range msgs {
		switch m.Role {
		case agent.RoleUser:
			msg, err := convertUser(m.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, msg)

		case agent.RoleAssistant:
			out = append(out, convertAssistant(m.Content))

		case agent.RoleTool:
			for _, b := range m.Content {
				if b.ToolResult == nil {
					continue
				}
				out = append(out, sdk.ToolMessage(b.ToolResult.Content, b.ToolResult.ToolUseID))
			}
		}
	}
	return out, nil
}

func convertUser(blocks []agent.Block) (sdk.ChatCompletionMessageParamUnion, error) {
	// Fast path: single text block.
	if len(blocks) == 1 && blocks[0].Text != nil {
		return sdk.UserMessage(blocks[0].Text.Text), nil
	}
	var parts []sdk.ChatCompletionContentPartUnionParam
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			parts = append(parts, sdk.TextContentPart(b.Text.Text))
		case b.Image != nil:
			dataURL := "data:" + b.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(b.Image.Data)
			parts = append(parts, sdk.ImageContentPart(sdk.ChatCompletionContentPartImageImageURLParam{
				URL: dataURL,
			}))
		}
	}
	return sdk.UserMessage(parts), nil
}

func convertAssistant(blocks []agent.Block) sdk.ChatCompletionMessageParamUnion {
	assistant := sdk.ChatCompletionAssistantMessageParam{}
	var textBuf strings.Builder
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			textBuf.WriteString(b.Text.Text)
		case b.ToolUse != nil:
			assistant.ToolCalls = append(assistant.ToolCalls, sdk.ChatCompletionMessageToolCallParam{
				ID: b.ToolUse.ID,
				Function: sdk.ChatCompletionMessageToolCallFunctionParam{
					Name:      b.ToolUse.Name,
					Arguments: string(b.ToolUse.Input),
				},
			})
		}
		// Thinking blocks are dropped — OpenAI's Chat Completions API doesn't
		// accept reasoning payloads on replay.
	}
	if textBuf.Len() > 0 {
		assistant.Content.OfString = sdk.String(textBuf.String())
	}
	return sdk.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}
