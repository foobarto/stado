package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

const verifyToolName = "shell__exec"

type VerifyStatus string

const (
	VerifyStarted         VerifyStatus = "started"
	VerifyPending         VerifyStatus = "pending_candidate"
	VerifyDeferred        VerifyStatus = "tool_continuation"
	VerifyPassed          VerifyStatus = "passed"
	VerifyFailed          VerifyStatus = "failed"
	VerifyInfrastructure  VerifyStatus = "infrastructure_error"
	VerifyCancelled       VerifyStatus = "cancelled"
	VerifyGenerationError VerifyStatus = "generation_error"
	VerifyExhausted       VerifyStatus = "verify_exhausted"
)

const (
	maxBufferedVerifyEvents = 65536
	maxBufferedVerifyBytes  = 16 << 20
)

func bufferVerifyEvent(events *[]agent.Event, totalBytes *int, event agent.Event) error {
	size := len(event.Text) + len(event.ThinkingSig) + len(event.ToolArgsDelta) + len(event.Native)
	if event.ToolCall != nil {
		size += len(event.ToolCall.ID) + len(event.ToolCall.Name) + len(event.ToolCall.Input)
	}
	if len(*events) >= maxBufferedVerifyEvents || *totalBytes+size > maxBufferedVerifyBytes {
		return fmt.Errorf("runtime: buffered verification candidate exceeded event budget")
	}
	*events = append(*events, event)
	*totalBytes += size
	return nil
}

type VerifyConfig struct {
	Commands  []string
	MaxRounds int
	Strict    bool
}

func VerifyConfigFrom(cfg *config.Config) VerifyConfig {
	if cfg == nil {
		return VerifyConfig{}
	}
	commands := make([]string, 0, len(cfg.Verify.Commands))
	for _, command := range cfg.Verify.Commands {
		if command = strings.TrimSpace(command); command != "" {
			commands = append(commands, command)
		}
	}
	maxRounds := cfg.Verify.MaxRounds
	if len(commands) > 0 && maxRounds <= 0 {
		maxRounds = 3
	}
	return VerifyConfig{Commands: commands, MaxRounds: maxRounds, Strict: cfg.Verify.Strict}
}

func (c VerifyConfig) Enabled() bool { return len(c.Commands) > 0 && c.MaxRounds > 0 }

type VerifyEvent struct {
	Status       VerifyStatus
	Round        int
	Command      string
	Output       string
	EvidenceRefs []string
}

type VerifyOutcome struct {
	Status   VerifyStatus
	Round    int
	Command  string
	Output   string
	Feedback string
	Err      error
}

var ErrVerifyExhausted = errors.New("runtime: verification exhausted")

type VerifyExhaustedError struct {
	Round    int
	Command  string
	Feedback string
}

func (e *VerifyExhaustedError) Error() string {
	return fmt.Sprintf("%v after %d round(s): %s", ErrVerifyExhausted, e.Round, e.Feedback)
}

func (e *VerifyExhaustedError) Unwrap() error { return ErrVerifyExhausted }

// RunVerificationRound runs ordered command gates through Executor.Run, so
// hooks, audit, sandboxing, metrics, and tool-result budgets are identical to
// a model-issued shell call. The first failing gate short-circuits the round.
func RunVerificationRound(ctx context.Context, executor *tools.Executor, host tool.Host, cfg VerifyConfig, round int, onEvent func(VerifyEvent)) VerifyOutcome {
	if !cfg.Enabled() {
		return VerifyOutcome{Status: VerifyPassed, Round: round}
	}
	if executor == nil || executor.Registry == nil {
		return verifyInfrastructure(round, "", "verification executor unavailable", nil, onEvent)
	}
	if _, ok := executor.Registry.Get(verifyToolName); !ok {
		return verifyInfrastructure(round, "", verifyToolName+" is disabled or unavailable", nil, onEvent)
	}
	for _, command := range cfg.Commands {
		emitVerify(onEvent, VerifyEvent{Status: VerifyStarted, Round: round, Command: command})
		args, _ := json.Marshal(map[string]any{
			"command":    command,
			"timeout_ms": 300000,
		})
		res, evidence, err := executor.RunWithEvidence(ctx, verifyToolName, args, host)
		refs := verificationExecutionEvidenceRefs(evidence)
		if ctxErr := ctx.Err(); ctxErr != nil {
			out := VerifyOutcome{
				Status: VerifyCancelled, Round: round, Command: command,
				Output: ctxErr.Error(), Feedback: ctxErr.Error(), Err: ctxErr,
			}
			emitVerify(onEvent, VerifyEvent{Status: out.Status, Round: round, Command: command, Output: out.Output, EvidenceRefs: refs})
			return out
		}
		if res.Error != "" {
			if res.FailureKind == tool.FailureLaunch {
				return verifyInfrastructureWithEvidence(round, command, res.Error, errors.New(res.Error), refs, onEvent)
			}
			feedback := fmt.Sprintf("Verification command failed: %s\n%s", command, res.Error)
			out := VerifyOutcome{
				Status: VerifyFailed, Round: round, Command: command,
				Output: res.Error, Feedback: feedback,
			}
			emitVerify(onEvent, VerifyEvent{Status: out.Status, Round: round, Command: command, Output: res.Error, EvidenceRefs: refs})
			return out
		}
		if err != nil {
			return verifyInfrastructureWithEvidence(round, command, err.Error(), err, refs, onEvent)
		}
		emitVerify(onEvent, VerifyEvent{Status: VerifyPassed, Round: round, Command: command, Output: res.Content, EvidenceRefs: refs})
	}
	return VerifyOutcome{Status: VerifyPassed, Round: round}
}

func verifyInfrastructure(round int, command, output string, err error, onEvent func(VerifyEvent)) VerifyOutcome {
	return verifyInfrastructureWithEvidence(round, command, output, err, nil, onEvent)
}

func verifyInfrastructureWithEvidence(round int, command, output string, err error, refs []string, onEvent func(VerifyEvent)) VerifyOutcome {
	out := VerifyOutcome{
		Status: VerifyInfrastructure, Round: round, Command: command,
		Output: output, Feedback: output, Err: err,
	}
	emitVerify(onEvent, VerifyEvent{Status: out.Status, Round: round, Command: command, Output: output, EvidenceRefs: refs})
	return out
}

func verificationExecutionEvidenceRefs(evidence tools.ExecutionEvidence) []string {
	refs := make([]string, 0, 2)
	if evidence.TraceRef != "" {
		refs = append(refs, evidence.TraceRef)
	}
	if evidence.TreeRef != "" {
		refs = append(refs, evidence.TreeRef)
	}
	return refs
}

func emitVerify(fn func(VerifyEvent), event VerifyEvent) {
	if fn != nil {
		fn(event)
	}
}

func emitVerifyGenerationEnd(fn func(VerifyEvent), round int, err error) {
	status := VerifyGenerationError
	if errors.Is(err, context.Canceled) {
		status = VerifyCancelled
	}
	output := "candidate generation ended before verification"
	if err != nil {
		output = err.Error()
	}
	emitVerify(fn, VerifyEvent{Status: status, Round: round, Output: output})
}
