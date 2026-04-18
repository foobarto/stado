// Package google is the direct google/generative-ai-go (Gemini) implementation
// of pkg/agent.Provider.
//
// Supports Gemini text + function calling + multimodal inputs. Gemini 2.5
// "thinking" is not exposed through the v0.20.1 SDK, so SupportsThinking is
// false; circle back when the SDK surfaces it.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/foobarto/stado/pkg/agent"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type Provider struct {
	client *genai.Client
	name   string
}

func New(apiKey string) (*Provider, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("google: GEMINI_API_KEY not set")
	}
	client, err := genai.NewClient(context.Background(), option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("google: create client: %w", err)
	}
	return &Provider{client: client, name: "google"}, nil
}

func (p *Provider) Close() error { return p.client.Close() }

func (p *Provider) Name() string { return p.name }

func (p *Provider) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsPromptCache:  false,
		SupportsThinking:     false,
		MaxParallelToolCalls: 1,
		SupportsVision:       true,
		MaxContextTokens:     1_000_000,
	}
}

func (p *Provider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	model := p.client.GenerativeModel(req.Model)
	if req.System != "" {
		model.SystemInstruction = genai.NewUserContent(genai.Text(req.System))
	}
	if req.Temperature != nil {
		v := float32(*req.Temperature)
		model.Temperature = &v
	}
	if req.MaxTokens > 0 {
		v := int32(req.MaxTokens)
		model.MaxOutputTokens = &v
	}
	if len(req.Tools) > 0 {
		tools, err := buildTools(req.Tools)
		if err != nil {
			return nil, err
		}
		model.Tools = tools
	}

	history, current, err := splitMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	session := model.StartChat()
	session.History = history

	ch := make(chan agent.Event, 16)
	go streamSession(ctx, session, current, ch)
	return ch, nil
}

// splitMessages returns history (all but the last user message group) and the
// parts to send in this turn. Gemini's SendMessage takes fresh turn content;
// prior turns go in History.
func splitMessages(msgs []agent.Message) ([]*genai.Content, []genai.Part, error) {
	var history []*genai.Content
	var lastUserParts []genai.Part
	lastIsUser := false

	for i, m := range msgs {
		parts, err := convertContent(m.Content, m.Role)
		if err != nil {
			return nil, nil, err
		}
		if len(parts) == 0 {
			continue
		}
		role := geminiRole(m.Role)
		if i == len(msgs)-1 && role == "user" {
			lastUserParts = parts
			lastIsUser = true
			break
		}
		history = append(history, &genai.Content{Role: role, Parts: parts})
	}
	if !lastIsUser {
		return nil, nil, errors.New("google: last message must be user role for SendMessage")
	}
	return history, lastUserParts, nil
}

func geminiRole(r agent.Role) string {
	switch r {
	case agent.RoleAssistant:
		return "model"
	case agent.RoleTool:
		return "function"
	default:
		return "user"
	}
}

func convertContent(blocks []agent.Block, role agent.Role) ([]genai.Part, error) {
	var parts []genai.Part
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			parts = append(parts, genai.Text(b.Text.Text))
		case b.Image != nil:
			parts = append(parts, genai.ImageData(mediaSubtype(b.Image.MediaType), b.Image.Data))
		case b.ToolUse != nil:
			var args map[string]any
			if len(b.ToolUse.Input) > 0 {
				if err := json.Unmarshal(b.ToolUse.Input, &args); err != nil {
					return nil, fmt.Errorf("google: tool_use input: %w", err)
				}
			}
			parts = append(parts, genai.FunctionCall{Name: b.ToolUse.Name, Args: args})
		case b.ToolResult != nil:
			// Gemini's FunctionResponse takes a name + response map; we can't
			// recover the function name from ToolUseID alone, so the caller
			// should provide it. Use ToolUseID as a fallback.
			parts = append(parts, genai.FunctionResponse{
				Name:     b.ToolResult.ToolUseID,
				Response: map[string]any{"result": b.ToolResult.Content},
			})
		}
	}
	_ = role
	return parts, nil
}

func mediaSubtype(mt string) string {
	// "image/png" → "png"
	for i := 0; i < len(mt); i++ {
		if mt[i] == '/' {
			return mt[i+1:]
		}
	}
	return mt
}

func buildTools(defs []agent.ToolDef) ([]*genai.Tool, error) {
	var funcs []*genai.FunctionDeclaration
	for _, t := range defs {
		schema, err := jsonSchemaToGenai(t.Schema)
		if err != nil {
			return nil, fmt.Errorf("google: tool %q schema: %w", t.Name, err)
		}
		funcs = append(funcs, &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: funcs}}, nil
}

func streamSession(ctx context.Context, session *genai.ChatSession, parts []genai.Part, ch chan<- agent.Event) {
	defer close(ch)

	iter := session.SendMessageStream(ctx, parts...)
	var usage *agent.Usage

	for {
		resp, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			ch <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("google: %w", err)}
			return
		}
		if resp.UsageMetadata != nil {
			usage = &agent.Usage{
				InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
				OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			}
		}
		for _, cand := range resp.Candidates {
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				switch p := part.(type) {
				case genai.Text:
					ch <- agent.Event{Kind: agent.EvTextDelta, Text: string(p)}
				case genai.FunctionCall:
					args, _ := json.Marshal(p.Args)
					tc := &agent.ToolUseBlock{
						ID:    p.Name, // Gemini doesn't emit call ids; reuse name
						Name:  p.Name,
						Input: json.RawMessage(args),
					}
					ch <- agent.Event{Kind: agent.EvToolCallStart, ToolCall: tc}
					ch <- agent.Event{Kind: agent.EvToolCallArgsDelta, ToolArgsDelta: string(args)}
					ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: tc}
				}
			}
		}
	}

	ch <- agent.Event{Kind: agent.EvDone, Usage: usage}
}

// jsonSchemaToGenai converts a JSON Schema object to Gemini's schema. Only
// the subset stado tool descriptions use (type, properties, required, items,
// description) is handled.
func jsonSchemaToGenai(schemaJSON json.RawMessage) (*genai.Schema, error) {
	if len(schemaJSON) == 0 {
		return &genai.Schema{Type: genai.TypeObject}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(schemaJSON, &m); err != nil {
		return nil, err
	}
	return mapToSchema(m), nil
}

func mapToSchema(m map[string]any) *genai.Schema {
	s := &genai.Schema{}
	if t, ok := m["type"].(string); ok {
		s.Type = schemaType(t)
	}
	if d, ok := m["description"].(string); ok {
		s.Description = d
	}
	if props, ok := m["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for name, v := range props {
			if pm, ok := v.(map[string]any); ok {
				s.Properties[name] = mapToSchema(pm)
			}
		}
	}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if rs, ok := r.(string); ok {
				s.Required = append(s.Required, rs)
			}
		}
	}
	if items, ok := m["items"].(map[string]any); ok {
		s.Items = mapToSchema(items)
	}
	if enum, ok := m["enum"].([]any); ok {
		for _, e := range enum {
			if es, ok := e.(string); ok {
				s.Enum = append(s.Enum, es)
			}
		}
	}
	return s
}

func schemaType(t string) genai.Type {
	switch t {
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	default:
		return genai.TypeUnspecified
	}
}
