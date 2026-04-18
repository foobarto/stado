package provider

import (
	"context"
	"encoding/json"
)

type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

type Request struct {
	Messages []Message
	Model    string
	Tools    []ToolDef
}

type Message struct {
	Role    string
	Content string
}

type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type EventType int

const (
	EventTextDelta EventType = iota
	EventToolCallStart
	EventToolCallArgsDelta
	EventToolCallEnd
	EventUsage
	EventDone
	EventError
)

type Event struct {
	Type      EventType
	TextDelta string
	ToolCall  *ToolCall
	Usage     *Usage
	Err       error
}

type ToolCall struct {
	ID   string
	Name string
	Args string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}
