package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/foobarto/stado/pkg/provider"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type Gemini struct {
	client *genai.Client
}

func New(apiKey string) (*Gemini, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &Gemini{client: client}, nil
}

func (g *Gemini) Name() string {
	return "gemini"
}

func (g *Gemini) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)

	model := req.Model
	if model == "" {
		model = "gemini-2.0-flash"
	}

	gm := g.client.GenerativeModel(model)

	var history []*genai.Content
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			history = append(history, genai.NewUserContent(genai.Text(m.Content)))
		case "assistant":
			history = append(history, &genai.Content{
			Role:  "model",
			Parts: []genai.Part{genai.Text(m.Content)},
		})
		case "tool":
			var tr ToolResult
			if err := json.Unmarshal([]byte(m.Content), &tr); err != nil {
				continue
			}
			history = append(history, &genai.Content{
				Role:  "tool",
				Parts: []genai.Part{genai.FunctionResponse{Name: tr.Name, Response: map[string]any{"result": tr.Content}}},
			})
		}
	}

	if len(req.Tools) > 0 {
		var tools []*genai.Tool
		for _, t := range req.Tools {
			tools = append(tools, &genai.Tool{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  jsonSchemaToGenai(t.Parameters),
				}},
			})
		}
		gm.Tools = tools
	}

	go func() {
		defer close(ch)

		session := gm.StartChat()
		session.History = history

		iter := session.SendMessageStream(ctx)
		for {
			resp, err := iter.Next()
			if err != nil {
				ch <- provider.Event{
					Type: provider.EventError,
					Err:  fmt.Errorf("stream error: %w", err),
				}
				return
			}
			if resp == nil {
				break
			}

			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for _, part := range cand.Content.Parts {
					switch p := part.(type) {
					case genai.Text:
						ch <- provider.Event{
							Type:      provider.EventTextDelta,
							TextDelta: string(p),
						}
					case genai.FunctionCall:
						argsJSON, _ := json.Marshal(p.Args)
						tc := &provider.ToolCall{
							ID:   p.Name,
							Name: p.Name,
							Args: string(argsJSON),
						}
						ch <- provider.Event{Type: provider.EventToolCallStart, ToolCall: tc}
						ch <- provider.Event{Type: provider.EventToolCallEnd, ToolCall: tc}
					}
				}
			}
		}

		ch <- provider.Event{Type: provider.EventDone}
	}()

	return ch, nil
}

func jsonSchemaToGenai(schemaJSON json.RawMessage) *genai.Schema {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil
	}
	s := &genai.Schema{Type: genai.TypeObject}
	if props, ok := schema["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema)
		for name, prop := range props {
			if p, ok := prop.(map[string]any); ok {
				s.Properties[name] = mapToGenaiSchema(p)
			}
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			if rs, ok := r.(string); ok {
				s.Required = append(s.Required, rs)
			}
		}
	}
	return s
}

func mapToGenaiSchema(m map[string]any) *genai.Schema {
	s := &genai.Schema{}
	if typ, ok := m["type"].(string); ok {
		switch typ {
		case "string":
			s.Type = genai.TypeString
		case "number":
			s.Type = genai.TypeNumber
		case "integer":
			s.Type = genai.TypeInteger
		case "boolean":
			s.Type = genai.TypeBoolean
		case "array":
			s.Type = genai.TypeArray
		case "object":
			s.Type = genai.TypeObject
		}
	}
	if desc, ok := m["description"].(string); ok {
		s.Description = desc
	}
	return s
}

type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

func (g *Gemini) Close() error {
	return g.client.Close()
}

func (g *Gemini) ToolResultMessage(id, name, content string) provider.Message {
	result := ToolResult{ID: id, Name: name, Content: content}
	data, _ := json.Marshal(result)
	return provider.Message{Role: "tool", Content: string(data)}
}

func (g *Gemini) AssistantMessage(text string) provider.Message {
	return provider.Message{Role: "assistant", Content: text}
}

func (g *Gemini) UserMessage(text string) provider.Message {
	return provider.Message{Role: "user", Content: strings.TrimSpace(text)}
}
