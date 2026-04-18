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

// Classifier is an optional interface tools implement to declare their
// mutation class. Tools that don't implement it default to ClassNonMutating.
//
// Class drives the tree-vs-trace commit policy (PLAN.md §2.4):
//   - NonMutating (grep/glob/read) → trace-only
//   - Mutating (write/edit)        → trace + tree commit
//   - Exec (bash/shell)            → trace + tree-if-diff-nonempty
type Classifier interface {
	Class() Class
}

// Class enumerates the mutation classes.
type Class int

const (
	ClassNonMutating Class = iota
	ClassMutating
	ClassExec
)

// String renders a class for logs / commit-message metadata.
func (c Class) String() string {
	switch c {
	case ClassMutating:
		return "mutating"
	case ClassExec:
		return "exec"
	default:
		return "non-mutating"
	}
}

// ClassOf returns the class for any tool — falling back to NonMutating when
// the tool doesn't implement Classifier.
func ClassOf(t Tool) Class {
	if c, ok := t.(Classifier); ok {
		return c.Class()
	}
	return ClassNonMutating
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
