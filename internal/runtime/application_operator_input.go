package runtime

// Runtime-neutral native controller contract for broker-owned operator input.
// The application guest classifies input through its fixed host import; only
// the native session controller can capture immutable text or commit receiver
// delivery after that text is durable in the exact conversation.

import "context"

const MaxApplicationOperatorInputBytes = 48 << 10

type ApplicationOperatorInput struct {
	ID          string
	SessionID   string
	Generation  uint64
	PluginID    string
	RunID       string
	Ordinal     uint64
	Version     uint64
	WALSequence uint64
	Text        string
	Digest      string
	Status      string
	ReviewID    string
	Recovered   bool
}

type ApplicationContinuationInput struct {
	ID      string
	Ordinal uint64
	Text    string
	Digest  string
}

type ApplicationContinuation struct {
	CompletionID string
	DeliveryID   string
	SessionID    string
	Generation   uint64
	PluginID     string
	RunID        string
	InputIDs     []string
	Inputs       []ApplicationContinuationInput
	Status       string
	WALSequence  uint64
}

type ApplicationDeferredTask struct {
	ID      string
	InputID string
	RunID   string
	Ordinal uint64
	Title   string
	Status  string
}

type ApplicationInputState struct {
	AsOfSequence          uint64
	ActiveWorkerRun       *ApplicationWorkerRun
	ReadyInputs           []ApplicationOperatorInput
	ReviewingInputs       []ApplicationOperatorInput
	RecoveryInputs        []ApplicationOperatorInput
	OpenDeferredTaskCount int
	OpenDeferredTasks     []ApplicationDeferredTask
	OpenDeferredTruncated bool
	PendingContinuation   *ApplicationContinuation
}

// ApplicationOperatorInputController is implemented only by an authenticated
// native broker session. Capture failures are fail-closed when an application
// worker owns recurrence; callers must never reinterpret them as permission to
// submit ordinary unsupervised input.
type ApplicationOperatorInputController interface {
	CaptureApplicationOperatorInput(context.Context, string, string) (ApplicationOperatorInput, error)
	ApplicationOperatorInputState(context.Context) (ApplicationInputState, error)
	CommitApplicationOperatorInput(context.Context, ApplicationOperatorInput, bool) (ApplicationOperatorInput, error)
	CommitApplicationContinuation(context.Context, ApplicationContinuation) (ApplicationContinuation, error)
}
