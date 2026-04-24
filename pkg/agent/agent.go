// Package agent defines the coding-agent / provider seam.
//
// This is intentionally not an LLM-abstraction library. The interface encodes
// exactly what the stado agent loop needs: a streaming turn that can emit
// text, thinking, tool calls, and usage — with provider-native payloads
// preserved verbatim where it matters (thinking signatures round-trip).
//
// Four direct implementations consume this interface: anthropic, openai,
// google, and oaicompat (llama.cpp/vLLM/ollama/etc). No third-party
// abstraction layer.
package agent

import (
	"context"
	"encoding/json"
)

// Provider is the coding-agent seam. One streaming-turn method; agent loop
// branches on Capabilities rather than picking a lowest-common-denominator path.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	StreamTurn(ctx context.Context, req TurnRequest) (<-chan Event, error)
}

// TokenCounter is an optional interface providers implement when they can
// pre-flight count tokens for a TurnRequest. Detection is via type
// assertion so adding the interface doesn't break existing Provider impls.
//
// When a provider does not satisfy TokenCounter, interactive surfaces can
// still stream turns but should treat context-window percentages as
// unavailable until provider-reported Usage arrives.
type TokenCounter interface {
	// CountTokens returns the prompt-side token count for req, using the
	// provider's native tokenizer. Result covers system + messages + tools
	// (the "stable prefix"); output is estimated at generation time via
	// Usage.OutputTokens on EvDone. Best-effort: returns error on network
	// failures (where applicable).
	CountTokens(ctx context.Context, req TurnRequest) (int, error)
}

// Capabilities tells the agent loop what a provider can do on this model.
// Populated either statically (known provider) or via runtime probing
// (oaicompat /v1/models + first-call heuristics).
type Capabilities struct {
	SupportsPromptCache  bool
	SupportsThinking     bool
	MaxParallelToolCalls int
	SupportsVision       bool
	MaxContextTokens     int
}

// TurnRequest is one agent turn: model, messages, available tools, and
// provider-specific knobs the agent wants applied.
type TurnRequest struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolDef

	Temperature *float64
	MaxTokens   int

	// Thinking is non-nil to enable extended thinking where supported.
	Thinking *ThinkingConfig

	// CacheHints are breakpoints for Anthropic-style prompt caching. Indexes
	// refer to Messages. Ignored by providers that don't support caching.
	CacheHints []CachePoint
}

type ThinkingConfig struct {
	BudgetTokens int
}

type CachePoint struct {
	// MessageIndex: everything up through Messages[MessageIndex] is one cache
	// breakpoint (Anthropic's cache_control shape).
	MessageIndex int
}

// Role is the conversation role. "tool" is a user-role message that carries a
// tool_result block in OpenAI/Anthropic parlance; we keep it distinct here.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is an ordered list of content blocks belonging to one role.
// Multiple block kinds can appear in one assistant message (text + tool_use +
// thinking), which matters for faithful turn replay.
type Message struct {
	Role    Role
	Content []Block
}

// Block is the multimodal content unit. Exactly one of the pointer fields is
// non-nil. Using a sum-of-pointers (not an interface) keeps the wire
// representation ergonomic for JSON recording/replay in tests.
type Block struct {
	Text       *TextBlock       `json:"text,omitempty"`
	ToolUse    *ToolUseBlock    `json:"tool_use,omitempty"`
	ToolResult *ToolResultBlock `json:"tool_result,omitempty"`
	Image      *ImageBlock      `json:"image,omitempty"`
	Thinking   *ThinkingBlock   `json:"thinking,omitempty"`
}

type TextBlock struct {
	Text string `json:"text"`
}

type ToolUseBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type ImageBlock struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// ThinkingBlock carries reasoning content verbatim.
//
// Normalizing these away breaks extended-thinking tool-use round-trips.
// Signature is Anthropic's thinking-block signature that must be replayed
// unchanged. Native holds the raw provider-specific envelope for providers
// whose thinking shape doesn't fit (Text, Signature) — e.g. OpenAI o-series
// reasoning chunks or Gemini thoughts.
type ThinkingBlock struct {
	Text      string          `json:"text,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Native    json.RawMessage `json:"native,omitempty"`
}

// ToolDef is a tool declaration for this turn. Schema is a JSON Schema object.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// EventKind discriminates streaming events.
type EventKind int

const (
	EvTextDelta EventKind = iota
	EvThinkingDelta
	EvToolCallStart
	EvToolCallArgsDelta
	EvToolCallEnd
	EvCacheHit
	EvCacheMiss
	EvUsage
	EvDone
	EvError
)

// Event is a single streaming event from a turn. Kind selects which fields
// are populated; Native is an opaque provider-native payload available to
// callers that want to reconstruct exact provider structure (audit, replay).
type Event struct {
	Kind EventKind

	Text          string          // EvTextDelta, EvThinkingDelta
	ThinkingSig   string          // EvThinkingDelta — Anthropic sig chunk
	ToolCall      *ToolUseBlock   // EvToolCallStart / EvToolCallEnd (accumulated)
	ToolArgsDelta string          // EvToolCallArgsDelta — raw JSON fragment
	Usage         *Usage          // EvUsage, EvDone
	Err           error           // EvError
	Native        json.RawMessage // opaque, any kind
}

// Usage is per-turn accounting. CacheRead/CacheWrite track Anthropic-style
// prompt caching; zero on providers that don't report it.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
}

// Text is a shorthand for building a user/assistant message from plain text.
func Text(role Role, text string) Message {
	return Message{Role: role, Content: []Block{{Text: &TextBlock{Text: text}}}}
}
