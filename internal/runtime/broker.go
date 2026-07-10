package runtime

import "context"

// ContextTaint is the broker-visible provenance state for the current
// operator turn. Tool and subagent results are untrusted at ingestion; a new
// operator prompt resets the state to clean.
type ContextTaint string

const (
	ContextClean   ContextTaint = "clean"
	ContextTainted ContextTaint = "tainted"
)

// BrokerSubagentRequest is the runtime-owned child request shape. It avoids a
// dependency from runtime back to the CLI's concrete daemon client.
type BrokerSubagentRequest struct {
	Role       string
	Mode       string
	WriteScope []string
}

// BrokerController is the narrow orchestration contract runtime loops need.
// A created child is itself a controller so nested children inherit the same
// broker mediation and taint tracking.
type BrokerController interface {
	CreateSubagent(context.Context, BrokerSubagentRequest) (BrokerController, error)
	SetTaint(context.Context, ContextTaint) error
	Sandbox() ExecutorSandbox
	Worktree() string
	Close() error
}
