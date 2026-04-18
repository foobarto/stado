// Package anthropic is the direct anthropic-sdk-go implementation of
// pkg/agent.Provider.
//
// Features: prompt caching via cache_control breakpoints, extended thinking
// with signature round-trip, parallel tool calls, multimodal input.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/foobarto/stado/pkg/agent"
)

const defaultMaxTokens = 8192

type Provider struct {
	client sdk.Client
	name   string
}

func New(apiKey string) (*Provider, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: ANTHROPIC_API_KEY not set")
	}
	return &Provider{
		client: sdk.NewClient(option.WithAPIKey(apiKey)),
		name:   "anthropic",
	}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsPromptCache:  true,
		SupportsThinking:     true,
		MaxParallelToolCalls: 8,
		SupportsVision:       true,
		MaxContextTokens:     200_000,
	}
}

func (p *Provider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	messages, err := buildMessages(req)
	if err != nil {
		return nil, err
	}

	tools, err := buildTools(req.Tools)
	if err != nil {
		return nil, err
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(maxTokens(req.MaxTokens)),
		Messages:  messages,
		Tools:     tools,
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	if req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		params.Thinking = sdk.ThinkingConfigParamUnion{
			OfEnabled: &sdk.ThinkingConfigEnabledParam{
				BudgetTokens: int64(req.Thinking.BudgetTokens),
			},
		}
	}

	ch := make(chan agent.Event, 16)
	go stream(ctx, p.client.Messages.NewStreaming(ctx, params), ch)
	return ch, nil
}

// stream translates SDK events to agent.Events. Parallel tool calls are tracked
// by SDK `index`; thinking deltas and signatures round-trip verbatim.
func stream(_ context.Context, s *streamingResult, ch chan<- agent.Event) {
	defer close(ch)

	type pending struct {
		kind      string // "text" | "thinking" | "tool_use" | "redacted_thinking"
		id        string
		name      string
		args      []byte
		thinkText string
		signature string
		redacted  string
	}
	blocks := map[int64]*pending{}

	var finalUsage *agent.Usage

	for s.Next() {
		ev := s.Current()
		switch ev.Type {
		case "message_start":
			u := ev.Message.Usage
			finalUsage = &agent.Usage{
				InputTokens:      int(u.InputTokens),
				OutputTokens:     int(u.OutputTokens),
				CacheReadTokens:  int(u.CacheReadInputTokens),
				CacheWriteTokens: int(u.CacheCreationInputTokens),
			}

		case "content_block_start":
			cb := ev.ContentBlock
			p := &pending{kind: cb.Type, id: cb.ID, name: cb.Name}
			blocks[ev.Index] = p
			switch cb.Type {
			case "tool_use":
				ch <- agent.Event{
					Kind:     agent.EvToolCallStart,
					ToolCall: &agent.ToolUseBlock{ID: cb.ID, Name: cb.Name},
				}
			case "redacted_thinking":
				p.redacted = cb.Data
			}

		case "content_block_delta":
			p := blocks[ev.Index]
			if p == nil {
				continue
			}
			d := ev.Delta
			switch d.Type {
			case "text_delta":
				ch <- agent.Event{Kind: agent.EvTextDelta, Text: d.Text}
			case "thinking_delta":
				p.thinkText += d.Thinking
				ch <- agent.Event{Kind: agent.EvThinkingDelta, Text: d.Thinking}
			case "signature_delta":
				p.signature += d.Signature
				ch <- agent.Event{Kind: agent.EvThinkingDelta, ThinkingSig: d.Signature}
			case "input_json_delta":
				p.args = append(p.args, d.PartialJSON...)
				ch <- agent.Event{Kind: agent.EvToolCallArgsDelta, ToolArgsDelta: d.PartialJSON}
			}

		case "content_block_stop":
			p := blocks[ev.Index]
			if p == nil {
				continue
			}
			if p.kind == "tool_use" {
				ch <- agent.Event{
					Kind: agent.EvToolCallEnd,
					ToolCall: &agent.ToolUseBlock{
						ID:    p.id,
						Name:  p.name,
						Input: json.RawMessage(p.args),
					},
				}
			}

		case "message_delta":
			u := ev.Usage
			if finalUsage == nil {
				finalUsage = &agent.Usage{}
			}
			finalUsage.OutputTokens = int(u.OutputTokens)
		}
	}

	if err := s.Err(); err != nil {
		ch <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("anthropic: %w", err)}
		return
	}
	ch <- agent.Event{Kind: agent.EvDone, Usage: finalUsage}
}

type streamingResult = ssestream.Stream[sdk.MessageStreamEventUnion]

// buildMessages translates agent messages to MessageParam. Cache breakpoints
// get attached to the last block of the referenced message. Thinking blocks
// and their signatures round-trip verbatim so extended-thinking tool-use
// replays correctly.
func buildMessages(req agent.TurnRequest) ([]sdk.MessageParam, error) {
	cacheAt := map[int]bool{}
	for _, cp := range req.CacheHints {
		cacheAt[cp.MessageIndex] = true
	}

	var out []sdk.MessageParam
	for i, m := range req.Messages {
		blocks, err := convertBlocks(m.Content, m.Role)
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			continue
		}
		if cacheAt[i] {
			// Attach cache_control ephemeral to the last block of this message.
			setCacheControl(&blocks[len(blocks)-1])
		}
		switch m.Role {
		case agent.RoleUser, agent.RoleTool:
			out = append(out, sdk.NewUserMessage(blocks...))
		case agent.RoleAssistant:
			out = append(out, sdk.NewAssistantMessage(blocks...))
		}
	}
	return out, nil
}

func convertBlocks(blocks []agent.Block, role agent.Role) ([]sdk.ContentBlockParamUnion, error) {
	out := make([]sdk.ContentBlockParamUnion, 0, len(blocks))
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			out = append(out, sdk.NewTextBlock(b.Text.Text))
		case b.ToolUse != nil:
			var input any
			if len(b.ToolUse.Input) > 0 {
				if err := json.Unmarshal(b.ToolUse.Input, &input); err != nil {
					return nil, fmt.Errorf("anthropic: tool_use input: %w", err)
				}
			}
			out = append(out, sdk.NewToolUseBlock(b.ToolUse.ID, input, b.ToolUse.Name))
		case b.ToolResult != nil:
			out = append(out, sdk.NewToolResultBlock(
				b.ToolResult.ToolUseID,
				b.ToolResult.Content,
				b.ToolResult.IsError,
			))
		case b.Thinking != nil:
			// Round-trip thinking block verbatim. Redacted thinking arrives as
			// Native with a `data` field we don't inspect.
			if b.Thinking.Signature != "" || b.Thinking.Text != "" {
				out = append(out, sdk.NewThinkingBlock(b.Thinking.Signature, b.Thinking.Text))
			} else if len(b.Thinking.Native) > 0 {
				var payload struct {
					Data string `json:"data"`
				}
				if err := json.Unmarshal(b.Thinking.Native, &payload); err == nil && payload.Data != "" {
					out = append(out, sdk.NewRedactedThinkingBlock(payload.Data))
				}
			}
		case b.Image != nil:
			out = append(out, imageBlock(b.Image))
		}
	}
	_ = role
	return out, nil
}

func imageBlock(img *agent.ImageBlock) sdk.ContentBlockParamUnion {
	// Use the SDK's image source param. Accepting just the common media types.
	return sdk.ContentBlockParamUnion{
		OfImage: &sdk.ImageBlockParam{
			Source: sdk.ImageBlockParamSourceUnion{
				OfBase64: &sdk.Base64ImageSourceParam{
					Data:      string(img.Data),
					MediaType: sdk.Base64ImageSourceMediaType(img.MediaType),
				},
			},
		},
	}
}

func setCacheControl(b *sdk.ContentBlockParamUnion) {
	cc := sdk.NewCacheControlEphemeralParam()
	switch {
	case b.OfText != nil:
		b.OfText.CacheControl = cc
	case b.OfToolUse != nil:
		b.OfToolUse.CacheControl = cc
	case b.OfToolResult != nil:
		b.OfToolResult.CacheControl = cc
	case b.OfImage != nil:
		b.OfImage.CacheControl = cc
	}
}

func buildTools(defs []agent.ToolDef) ([]sdk.ToolUnionParam, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	out := make([]sdk.ToolUnionParam, 0, len(defs))
	for _, t := range defs {
		var schema map[string]any
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			return nil, fmt.Errorf("anthropic: tool %q schema: %w", t.Name, err)
		}
		input := sdk.ToolInputSchemaParam{
			Properties: schema["properties"],
			Required:   strSlice(schema["required"]),
		}
		tool := sdk.ToolParam{
			Name:        t.Name,
			Description: sdk.String(t.Description),
			InputSchema: input,
		}
		out = append(out, sdk.ToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

func maxTokens(n int) int {
	if n <= 0 {
		return defaultMaxTokens
	}
	return n
}

func strSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, a := range arr {
		out[i], _ = a.(string)
	}
	return out
}
