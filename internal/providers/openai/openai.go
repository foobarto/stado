package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/foobarto/stado/pkg/provider"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

type OpenAI struct {
	client openai.Client
}

func New(apiKey, baseURL string) (*OpenAI, error) {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &OpenAI{client: openai.NewClient(opts...)}, nil
}

func (o *OpenAI) Name() string {
	return "openai"
}

func (o *OpenAI) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)

	messages := buildMessages(req.Messages)

	model := req.Model
	if model == "" {
		model = "gpt-4o"
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(model),
		Messages: messages,
	}

	if len(req.Tools) > 0 {
		var tools []openai.ChatCompletionToolParam
		for _, t := range req.Tools {
			var schema map[string]any
			json.Unmarshal(t.Parameters, &schema)
			tools = append(tools, openai.ChatCompletionToolParam{
				Function: openai.FunctionDefinitionParam{
					Name:        t.Name,
					Description: openai.String(t.Description),
					Parameters:  openai.FunctionParameters(schema),
				},
			})
		}
		params.Tools = tools
	}

	go func() {
		defer close(ch)

		stream := o.client.Chat.Completions.NewStreaming(ctx, params)

		var currentToolCall *provider.ToolCall

		for stream.Next() {
			chunk := stream.Current()
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta

			if delta.Content != "" {
				ch <- provider.Event{
					Type:      provider.EventTextDelta,
					TextDelta: delta.Content,
				}
			}

			for _, tc := range delta.ToolCalls {
				if tc.ID != "" {
					currentToolCall = &provider.ToolCall{
						ID:   tc.ID,
						Name: tc.Function.Name,
					}
					ch <- provider.Event{
						Type:     provider.EventToolCallStart,
						ToolCall: currentToolCall,
					}
				}
				if currentToolCall != nil && tc.Function.Arguments != "" {
					currentToolCall.Args += tc.Function.Arguments
					ch <- provider.Event{
						Type:     provider.EventToolCallArgsDelta,
						ToolCall: &provider.ToolCall{Args: tc.Function.Arguments},
					}
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

		if currentToolCall != nil {
			ch <- provider.Event{
				Type:     provider.EventToolCallEnd,
				ToolCall: currentToolCall,
			}
		}

		ch <- provider.Event{Type: provider.EventDone}
	}()

	return ch, nil
}

func buildMessages(msgs []provider.Message) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, openai.UserMessage(m.Content))
		case "assistant":
			out = append(out, openai.AssistantMessage(m.Content))
		case "tool":
			var tr ToolResult
			if err := json.Unmarshal([]byte(m.Content), &tr); err != nil {
				continue
			}
			out = append(out, openai.ToolMessage(tr.Content, tr.ID))
		}
	}
	return out
}

type ToolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}
