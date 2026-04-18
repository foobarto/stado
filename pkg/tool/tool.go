package tool

import (
	"context"
	"encoding/json"
)

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Run(ctx context.Context, args json.RawMessage, h Host) (Result, error)
}

type Result struct {
	Content string
	Error   string
}

type Host interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
	Workdir() string
}

type ApprovalRequest struct {
	Tool    string
	Command string
	Args    map[string]any
}

type Decision int

const (
	DecisionAllow Decision = iota
	DecisionDeny
)
