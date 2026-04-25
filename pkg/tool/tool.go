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
// mutation class. Tools that don't implement it default to ClassExec so
// unknown tools are treated conservatively.
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

// ClassOf returns the class for any tool — falling back to Exec when the tool
// doesn't implement Classifier.
func ClassOf(t Tool) Class {
	if c, ok := t.(Classifier); ok {
		return c.Class()
	}
	return ClassExec
}

type Result struct {
	Content string
	Error   string
}

// Host is the read-write surface tools use to reach the runtime.
// PriorRead / RecordRead support in-turn read-dedup — see DESIGN §"Context
// management" → "In-turn deduplication". Only the read tool is expected to
// call these; other tools MUST NOT record against the read log even when
// they incidentally read files.
type Host interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
	Workdir() string

	// PriorRead returns the most recent prior read matching key, if any.
	// On ok=true, all fields of PriorReadInfo must be populated (non-zero
	// Turn, non-empty ContentHash). On ok=false, the returned value is
	// undefined — callers must inspect only ok. Hosts that don't support
	// dedup (tests, headless without a read log) always return ok=false.
	PriorRead(key ReadKey) (PriorReadInfo, bool)

	// RecordRead stores info keyed by key. Last-writer-wins under
	// concurrent calls — "most recent" is defined as RecordRead-call-order,
	// not issue-order. See DESIGN §"Context management" → "Concurrency".
	RecordRead(key ReadKey, info PriorReadInfo)
}

// WritePathGuard is an optional host capability for mutating file tools.
// Hosts that implement it can reject writes before the tool touches disk.
type WritePathGuard interface {
	CheckWritePath(path string) error
}

// ReadKey identifies a read for deduplication. Range is a canonical string:
// "" for full-file, "<start>:<end>" for ranged reads (1-indexed, inclusive).
// The read tool is responsible for resolving any alternative input shapes
// into this canonical form before constructing the ReadKey.
type ReadKey struct {
	Path  string
	Range string
}

// PriorReadInfo is what Host.PriorRead hands back on a match. All fields
// populated on ok=true; undefined on ok=false. Structs (rather than
// multiple return values) so future fields — hash algorithm, compression
// marker, … — don't force signature churn.
type PriorReadInfo struct {
	// Turn is the 1-indexed turn number when the prior read occurred.
	Turn int
	// ContentHash is the hex-encoded sha256 of the bytes returned to the
	// model in that turn. Scope is the targeted region only (for ranged
	// reads), not the full file.
	ContentHash string
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
