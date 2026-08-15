package runtime

// Generic broker-stamped lifecycle events (EP-0064).
//
// This file deliberately projects observations, not workflow conclusions. The
// host can attest that a provider returned text, tools ran, a verifier passed,
// or a session tree changed. Whether those facts mean "stalled", "unsafe",
// "needs a pivot", or "complete" is application policy and belongs in the
// signed WASM consumer. Keeping that distinction explicit is what prevents a
// future quality-gate application from quietly growing back into native Go.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	SessionTurnCommittedEvent    = "session.turn_committed"
	SessionTurnCommittedSchemaV1 = "stado.dev/session-turn-facts/v1"
	maxTurnFactTextBytes         = 4096
	maxTurnFactPaths             = 64
	maxTurnFactTools             = 128
	maxTurnFactDiffBytes         = 64 << 10
)

// SessionTurnCommittedV1 is the bounded host-fact projection delivered after
// one provider iteration has reached a durable continuation boundary. Session
// identity and generation remain outside this payload in the broker-authored
// lifecycle envelope; guests cannot choose either authority field.
type SessionTurnCommittedV1 struct {
	Schema            string                    `json:"schema"`
	Anchor            SessionTurnAnchorV1       `json:"anchor"`
	ToolOutcomes      []ToolOutcomeFactsV1      `json:"tool_outcomes,omitempty"`
	ProviderTokens    ProviderTurnFactsV1       `json:"provider_tokens"`
	VerificationFacts []VerificationFactsV1     `json:"verification_facts,omitempty"`
	TreeDiff          *SessionTreeChangeFactsV1 `json:"tree_diff,omitempty"`
	Assistant         AssistantTurnFactsV1      `json:"assistant"`
}

// SessionTurnAnchorV1 names immutable audit heads plus the operator-turn and
// provider-iteration coordinates. TreeHead and TraceHead are commit hashes,
// not guest prose and not application-maintained counters.
type SessionTurnAnchorV1 struct {
	SessionSequence uint64 `json:"session_sequence"`
	TurnRef         string `json:"turn_ref"`
	TreeDigest      string `json:"tree_digest"`
}

type ProviderTurnFactsV1 struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CachedTokens int `json:"cached_tokens,omitempty"`
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

type AssistantTurnFactsV1 struct {
	MessageRef string `json:"message_ref"`
	Digest     string `json:"digest"`
	Excerpt    string `json:"excerpt,omitempty"`
}

type ToolOutcomeFactsV1 struct {
	ID               string   `json:"id"`
	Tool             string   `json:"tool"`
	Class            string   `json:"class,omitempty"`
	CallDigest       string   `json:"call_digest"`
	ArgsDigest       string   `json:"args_digest"`
	ResultDigest     string   `json:"result_digest"`
	Outcome          string   `json:"outcome"`
	ErrorFingerprint string   `json:"error_fingerprint,omitempty"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
}

type VerificationFactsV1 struct {
	ID            string   `json:"id"`
	CommandDigest string   `json:"command_digest"`
	ResultDigest  string   `json:"result_digest"`
	Outcome       string   `json:"outcome"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
}

type SessionTreeChangeFactsV1 struct {
	BeforeDigest string   `json:"before_digest"`
	AfterDigest  string   `json:"after_digest"`
	DiffRef      string   `json:"diff_ref"`
	DiffDigest   string   `json:"diff_digest"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	Bytes        int64    `json:"bytes,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// TurnCommitInput contains only inputs already measured by the active runtime
// loop. The builder hashes all potentially large or attacker-controlled values
// and bounds the small excerpts before they enter the broker WAL.
type TurnCommitInput struct {
	Iteration        int
	TreeBefore       plumbing.Hash
	ProviderName     string
	Model            string
	Usage            agent.Usage
	CumulativeTokens int
	TokenLimit       int
	Text             string
	Calls            []agent.ToolUseBlock
	Results          []agent.ToolResultBlock
	// ToolClasses are host registry metadata (non-mutating, state-mutating,
	// mutating, exec), never a model or guest classification.
	ToolClasses  map[string]string
	Verification *VerifyOutcome
	Duration     time.Duration
}

// PublishSessionTurnCommitted publishes one idempotent broker event. A nil
// publisher is a supported no-op for brokerless development/test surfaces.
func PublishSessionTurnCommitted(ctx context.Context, publisher ApplicationEventPublisher, session *stadogit.Session, input TurnCommitInput) (uint64, error) {
	if publisher == nil {
		return 0, nil
	}
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return 0, errors.New("runtime: session.turn_committed requires a session")
	}
	facts, refs, err := BuildSessionTurnCommitted(session, input)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return 0, fmt.Errorf("runtime: encode session.turn_committed: %w", err)
	}
	digest := digestFact(raw)
	return publisher.PublishApplicationEvent(ctx, HostApplicationEvent{
		ID:   SessionTurnCommittedEvent + ":" + strings.TrimPrefix(digest, "sha256:"),
		Kind: SessionTurnCommittedEvent, Data: raw, EvidenceRefs: refs,
		IdempotencyKey: SessionTurnCommittedEvent + ":" + digest,
	})
}

func BuildSessionTurnCommitted(session *stadogit.Session, input TurnCommitInput) (SessionTurnCommittedV1, []string, error) {
	if session == nil {
		return SessionTurnCommittedV1{}, nil, errors.New("runtime: turn facts require a session")
	}
	if input.Iteration < 0 || len(input.Calls) > maxTurnFactTools || len(input.Results) > maxTurnFactTools {
		return SessionTurnCommittedV1{}, nil, errors.New("runtime: turn facts exceed iteration/tool bounds")
	}
	treeAfter, err := session.TreeHead()
	if err != nil {
		return SessionTurnCommittedV1{}, nil, fmt.Errorf("runtime: observe turn tree head: %w", err)
	}
	traceAfter, err := session.TraceHead()
	if err != nil {
		return SessionTurnCommittedV1{}, nil, fmt.Errorf("runtime: observe turn trace head: %w", err)
	}
	turn := session.Turn() + 1
	iteration := input.Iteration + 1
	// The durable operator-turn counter is the high word and the bounded
	// provider continuation is the low word. Broker WAL sequence remains the
	// cross-application ordering authority in the outer lifecycle envelope.
	sessionSequence := uint64(turn)<<32 | uint64(iteration)
	// A provider iteration can end on tool calls before the operator-turn tag
	// exists, so do not fabricate a refs/.../turns/N path here. Bind the
	// observation to the exact immutable tree-ref commit that the host saw.
	// The human coordinates remain a fragment; generic reviewer spawning pins
	// the same tree head synchronously before the worker can advance.
	treeVersion := "empty"
	if !treeAfter.IsZero() {
		treeVersion = treeAfter.String()
	}
	turnRef := fmt.Sprintf("git:%s@%s#turn-%d-iteration-%d", stadogit.TreeRef(session.ID), treeVersion, turn, iteration)
	treeDigest := applicationFactHashString(treeAfter)
	messageRef := turnRef + ":assistant"
	refs := make([]string, 0, 2)
	if !treeAfter.IsZero() {
		refs = append(refs, "git:"+stadogit.TreeRef(session.ID).String()+"@"+treeAfter.String())
	}
	if !traceAfter.IsZero() {
		refs = append(refs, "git:"+stadogit.TraceRef(session.ID).String()+"@"+traceAfter.String())
	}
	treeDiff, err := observeTreeChange(session, input.TreeBefore, treeAfter, refs)
	if err != nil {
		return SessionTurnCommittedV1{}, nil, err
	}

	text := boundedFactText(input.Text, maxTurnFactTextBytes)
	facts := SessionTurnCommittedV1{
		Schema: SessionTurnCommittedSchemaV1,
		Anchor: SessionTurnAnchorV1{
			SessionSequence: sessionSequence, TurnRef: turnRef, TreeDigest: treeDigest,
		},
		ProviderTokens: ProviderTurnFactsV1{
			InputTokens: nonNegative(input.Usage.InputTokens), OutputTokens: nonNegative(input.Usage.OutputTokens),
			CachedTokens: nonNegative(input.Usage.CacheReadTokens), BudgetTokens: nonNegative(input.TokenLimit),
		},
		Assistant: AssistantTurnFactsV1{
			MessageRef: messageRef, Digest: digestFact([]byte(input.Text)), Excerpt: text.text,
		},
		ToolOutcomes: buildToolFacts(input.Calls, input.Results, input.ToolClasses, refs), TreeDiff: treeDiff,
	}
	if input.Verification != nil {
		facts.VerificationFacts = []VerificationFactsV1{{
			ID:            fmt.Sprintf("%s:verification-%d", turnRef, input.Verification.Round),
			CommandDigest: digestFact([]byte(input.Verification.Command)),
			ResultDigest:  digestFact([]byte(verificationResultMaterial(*input.Verification))),
			Outcome:       verificationFactOutcome(input.Verification.Status), EvidenceRefs: append([]string(nil), refs...),
		}}
	}
	return facts, refs, nil
}

func observeTreeChange(session *stadogit.Session, before, after plumbing.Hash, evidenceRefs []string) (*SessionTreeChangeFactsV1, error) {
	if session == nil || before == after {
		return nil, nil
	}
	beforeTree, afterTree := plumbing.ZeroHash, plumbing.ZeroHash
	var err error
	if !before.IsZero() {
		beforeTree, err = session.TreeFromCommit(before)
		if err != nil {
			return nil, fmt.Errorf("runtime: observe pre-turn tree: %w", err)
		}
	}
	if !after.IsZero() {
		afterTree, err = session.TreeFromCommit(after)
		if err != nil {
			return nil, fmt.Errorf("runtime: observe post-turn tree: %w", err)
		}
	}
	paths, err := session.ChangedFilesBetween(beforeTree, afterTree)
	if err != nil {
		return nil, fmt.Errorf("runtime: observe changed paths: %w", err)
	}
	if len(paths) > maxTurnFactPaths {
		paths = paths[:maxTurnFactPaths]
	}
	patch, err := session.PatchBetweenHeads(before, after, maxTurnFactDiffBytes)
	if err != nil {
		return nil, fmt.Errorf("runtime: observe bounded diff: %w", err)
	}
	beforeDigest, afterDigest := applicationFactHashString(before), applicationFactHashString(after)
	return &SessionTreeChangeFactsV1{
		BeforeDigest: beforeDigest, AfterDigest: afterDigest,
		DiffRef: fmt.Sprintf("git-diff:%s..%s", beforeDigest, afterDigest), DiffDigest: digestFact([]byte(patch)),
		ChangedPaths: append([]string(nil), paths...), Bytes: int64(len(patch)), EvidenceRefs: append([]string(nil), evidenceRefs...),
	}, nil
}

func buildToolFacts(calls []agent.ToolUseBlock, results []agent.ToolResultBlock, classes map[string]string, evidenceRefs []string) []ToolOutcomeFactsV1 {
	byID := make(map[string]agent.ToolResultBlock, len(results))
	for _, result := range results {
		byID[result.ToolUseID] = result
	}
	out := make([]ToolOutcomeFactsV1, 0, len(calls))
	for _, call := range calls {
		result, ok := byID[call.ID]
		content := result.Content
		if !ok {
			content = "host result unavailable"
			result.IsError = true
		}
		item := ToolOutcomeFactsV1{
			ID: boundedFactText(call.ID, 256).text, Tool: boundedFactText(call.Name, 256).text,
			Class:      boundedFactText(classes[call.Name], 64).text,
			CallDigest: digestFact(append(append([]byte(call.Name), 0), call.Input...)),
			ArgsDigest: digestFact(call.Input), ResultDigest: digestFact([]byte(content)), Outcome: "success",
			EvidenceRefs: append([]string(nil), evidenceRefs...),
		}
		if result.IsError {
			item.Outcome = "error"
			item.ErrorFingerprint = digestFact([]byte(strings.TrimSpace(content)))
		}
		out = append(out, item)
	}
	return out
}

func verificationFactOutcome(status VerifyStatus) string {
	switch status {
	case VerifyPassed:
		return "pass"
	case VerifyFailed, VerifyExhausted:
		return "fail"
	case VerifyCancelled:
		return "cancelled"
	default:
		return "error"
	}
}

func verificationResultMaterial(outcome VerifyOutcome) string {
	material := outcome.Output + "\n" + outcome.Feedback
	if outcome.Err != nil {
		material += "\n" + outcome.Err.Error()
	}
	return material
}

type boundedText struct {
	text      string
	truncated bool
}

func boundedFactText(value string, limit int) boundedText {
	if limit < 0 || len(value) <= limit {
		return boundedText{text: value}
	}
	return boundedText{text: value[:limit], truncated: true}
}

func digestFact(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func applicationFactHashString(hash plumbing.Hash) string {
	if hash.IsZero() {
		return "empty"
	}
	return hash.String()
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
