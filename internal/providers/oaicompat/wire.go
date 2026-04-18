package oaicompat

import "encoding/json"

// Wire types for OpenAI Chat Completions API (the lingua franca for
// llama.cpp, vLLM, ollama, LiteLLM, OpenRouter, Groq, DeepSeek, Mistral, xAI).

type chatRequest struct {
	Model       string         `json:"model"`
	Messages    []chatMessage  `json:"messages"`
	Stream      bool           `json:"stream"`
	StreamOpts  *streamOptions `json:"stream_options,omitempty"`
	Tools       []chatTool     `json:"tools,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatTool struct {
	Type     string           `json:"type"` // "function"
	Function chatFunctionSpec `json:"function"`
}

type chatFunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatMessage struct {
	Role       string            `json:"role"`
	Content    any               `json:"content,omitempty"` // string | []contentPart | null
	ToolCalls  []chatToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
}

type contentPart struct {
	Type     string        `json:"type"` // "text" | "image_url"
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
}

type imageURLPart struct {
	URL string `json:"url"` // data: URL for inline images
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"` // "function"
	Function chatFunctionCall `json:"function"`
	Index    *int             `json:"index,omitempty"` // streaming: identifies parallel calls
}

type chatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// Streaming response chunk.
type chatChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usageWire   `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int       `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason,omitempty"`
}

type chunkDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"` // DeepSeek R1, some OAI o-series proxies
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
}

type usageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Models list (for probing).
type modelsList struct {
	Data []modelInfo `json:"data"`
}

type modelInfo struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length,omitempty"` // llama.cpp extension
}
