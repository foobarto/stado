package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/foobarto/stado/pkg/provider"
)

type Anthropic struct {
	client anthropic.Client
}

func New(apiKey string) (*Anthropic, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	return &Anthropic{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
	}, nil
}

func (a *Anthropic) Name() string {
	return "anthropic"
}

func (a *Anthropic) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)

	messages, err := buildMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	model := req.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	var tools []anthropic.ToolUnionParam
	for _, t := range req.Tools {
		schema := make(map[string]any)
		if err := json.Unmarshal(t.Parameters, &schema); err != nil {
			return nil, fmt.Errorf("tool schema: %w", err)
		}
		inputSchema := anthropic.ToolInputSchemaParam{
			Properties: schema["properties"],
			Required:   strSlice(schema["required"]),
		}
		tools = append(tools, anthropic.ToolUnionParamOfTool(inputSchema, t.Name))
	}

	go func() {
		defer close(ch)

		stream := a.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(model),
			MaxTokens: 8192,
			Messages:  messages,
			Tools:     tools,
		})

		for stream.Next() {
			ev := stream.Current()

			switch ev.Type {
			case "content_block_start":
				cb := ev.ContentBlock
				if cb.Type == "tool_use" {
					ch <- provider.Event{
						Type: provider.EventToolCallStart,
						ToolCall: &provider.ToolCall{
							ID:   cb.ID,
							Name: cb.Name,
						},
					}
				}

			case "content_block_delta":
				delta := ev.Delta
				if delta.Type == "text_delta" {
					ch <- provider.Event{
						Type:      provider.EventTextDelta,
						TextDelta: delta.Text,
					}
				} else if delta.Type == "input_json_delta" {
					ch <- provider.Event{
						Type: provider.EventToolCallArgsDelta,
						ToolCall: &provider.ToolCall{
							Args: delta.PartialJSON,
						},
					}
				}

			case "content_block_stop":
				ch <- provider.Event{
					Type: provider.EventToolCallEnd,
				}
			}
		}

		if err := stream.Err(); err != nil {
			ch <- provider.Event{
				Type: provider.EventError,
				Err:  fmt.Errorf("stream error: %w", err),
			}
			return
		}

		ch <- provider.Event{Type: provider.EventDone}
	}()

	return ch, nil
}

func buildMessages(msgs []provider.Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		case "tool":
			var tr ToolResult
			if err := json.Unmarshal([]byte(m.Content), &tr); err != nil {
				return nil, fmt.Errorf("tool result: %w", err)
			}
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(tr.ID, tr.Content, tr.IsError),
			))
		}
	}
	return out, nil
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

type ToolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}
