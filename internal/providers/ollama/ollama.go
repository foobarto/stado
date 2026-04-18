package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/foobarto/stado/pkg/provider"
)

const defaultBaseURL = "http://localhost:11434"

type Ollama struct {
	baseURL string
	client  *http.Client
}

func New(baseURL string) (*Ollama, error) {
	if baseURL == "" {
		baseURL = os.Getenv("OLLAMA_HOST")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Ollama{
		baseURL: baseURL,
		client:  &http.Client{},
	}, nil
}

func (o *Ollama) Name() string {
	return "ollama"
}

func (o *Ollama) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)

	model := req.Model
	if model == "" {
		model = "llama3.1"
	}

	payload := chatRequest{
		Model:    model,
		Messages: buildMessages(req.Messages),
		Stream:   true,
	}

	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			var params map[string]any
			json.Unmarshal(t.Parameters, &params)
			payload.Tools = append(payload.Tools, toolDef{
				Type: "function",
				Function: functionDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  params,
				},
			})
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			ch <- provider.Event{
				Type: provider.EventError,
				Err:  fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(bodyBytes)),
			}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var chunk chatResponse
			if err := json.Unmarshal(line, &chunk); err != nil {
				continue
			}

			if chunk.Message.Content != "" {
				ch <- provider.Event{
					Type:      provider.EventTextDelta,
					TextDelta: chunk.Message.Content,
				}
			}

			for _, tc := range chunk.Message.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				ch <- provider.Event{
					Type: provider.EventToolCallStart,
					ToolCall: &provider.ToolCall{
						ID:   tc.Function.Name,
						Name: tc.Function.Name,
						Args: string(argsJSON),
					},
				}
				ch <- provider.Event{
					Type: provider.EventToolCallEnd,
					ToolCall: &provider.ToolCall{
						ID:   tc.Function.Name,
						Name: tc.Function.Name,
						Args: string(argsJSON),
					},
				}
			}

			if chunk.Done {
				break
			}
		}

		ch <- provider.Event{Type: provider.EventDone}
	}()

	return ch, nil
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Tools    []toolDef `json:"tools,omitempty"`
}

type message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type toolDef struct {
	Type     string       `json:"type"`
	Function functionDef  `json:"function"`
}

type functionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolCall struct {
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type chatResponse struct {
	Model   string  `json:"model"`
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		ToolCalls []toolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done bool `json:"done"`
}

func buildMessages(msgs []provider.Message) []message {
	var out []message
	for _, m := range msgs {
		out = append(out, message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return out
}
