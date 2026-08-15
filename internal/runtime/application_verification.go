package runtime

// Runtime-neutral controller contract for lifecycle-application verification.
// Commands never enter these types: the TUI resolves operator configuration,
// executes it through the ordinary audited executor, and returns only facts.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type ApplicationVerificationSource struct {
	EventSequence   uint64 `json:"event_sequence"`
	SessionSequence uint64 `json:"session_sequence"`
	TurnRef         string `json:"turn_ref"`
	TreeDigest      string `json:"tree_digest"`
}

type ApplicationVerificationCommandFact struct {
	Ordinal            int      `json:"ordinal"`
	CommandDigest      string   `json:"command_digest"`
	ResultDigest       string   `json:"result_digest"`
	Outcome            string   `json:"outcome"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureFingerprint string   `json:"failure_fingerprint,omitempty"`
	EvidenceRefs       []string `json:"evidence_refs,omitempty"`
}

type ApplicationVerification struct {
	ID                 string                               `json:"id"`
	SessionID          string                               `json:"session_id"`
	Generation         uint64                               `json:"generation"`
	PluginID           string                               `json:"plugin_id"`
	RunID              string                               `json:"run_id"`
	WorkerVersion      uint64                               `json:"worker_version"`
	Version            uint64                               `json:"version"`
	WALSequence        uint64                               `json:"wal_sequence"`
	Status             string                               `json:"status"`
	Source             ApplicationVerificationSource        `json:"source"`
	SourceEvidenceRefs []string                             `json:"source_evidence_refs,omitempty"`
	SuiteDigest        string                               `json:"suite_digest,omitempty"`
	CommandDigests     []string                             `json:"command_digests,omitempty"`
	Outcome            string                               `json:"outcome,omitempty"`
	FailureKind        string                               `json:"failure_kind,omitempty"`
	FailureFingerprint string                               `json:"failure_fingerprint,omitempty"`
	Commands           []ApplicationVerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                             `json:"evidence_refs,omitempty"`
}

type ApplicationVerificationClaim struct {
	ID              string   `json:"id"`
	ExpectedVersion uint64   `json:"expected_version"`
	SuiteDigest     string   `json:"suite_digest"`
	CommandDigests  []string `json:"command_digests"`
}

type ApplicationVerificationFinish struct {
	ID                 string                               `json:"id"`
	ExpectedVersion    uint64                               `json:"expected_version"`
	Outcome            string                               `json:"outcome"`
	FailureKind        string                               `json:"failure_kind,omitempty"`
	FailureFingerprint string                               `json:"failure_fingerprint,omitempty"`
	Commands           []ApplicationVerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                             `json:"evidence_refs,omitempty"`
}

type applicationVerificationGetResponse struct {
	Found        bool                    `json:"found"`
	Verification ApplicationVerification `json:"verification,omitempty"`
}

func VerificationFactDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func VerificationSuiteDigest(commandDigests []string) string {
	raw, _ := json.Marshal(commandDigests)
	return VerificationFactDigest(string(raw))
}

func (a *LoadedLifecycleApplication) NextApplicationVerification(ctx context.Context) (ApplicationVerification, bool, error) {
	if a == nil || a.Controller == nil {
		return ApplicationVerification{}, false, errors.New("lifecycle application verification controller is unavailable")
	}
	response, err := a.Controller.CallApplicationController(ctx, "verification.get", []byte(`{}`))
	if err != nil {
		return ApplicationVerification{}, false, err
	}
	var result applicationVerificationGetResponse
	if err := json.Unmarshal(response, &result); err != nil {
		return ApplicationVerification{}, false, fmt.Errorf("lifecycle application verification response: %w", err)
	}
	if !result.Found {
		return ApplicationVerification{}, false, nil
	}
	if err := a.validateApplicationVerification(result.Verification); err != nil {
		return ApplicationVerification{}, false, err
	}
	return result.Verification, true, nil
}

func (a *LoadedLifecycleApplication) ClaimApplicationVerification(ctx context.Context, input ApplicationVerificationClaim) (ApplicationVerification, error) {
	return a.applicationVerificationTransition(ctx, "verification.claim", input)
}

func (a *LoadedLifecycleApplication) FinishApplicationVerification(ctx context.Context, input ApplicationVerificationFinish) (ApplicationVerification, error) {
	return a.applicationVerificationTransition(ctx, "verification.finish", input)
}

func (a *LoadedLifecycleApplication) applicationVerificationTransition(ctx context.Context, operation string, input any) (ApplicationVerification, error) {
	if a == nil || a.Controller == nil {
		return ApplicationVerification{}, errors.New("lifecycle application verification controller is unavailable")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ApplicationVerification{}, err
	}
	response, err := a.Controller.CallApplicationController(ctx, operation, payload)
	if err != nil {
		return ApplicationVerification{}, err
	}
	var record ApplicationVerification
	if err := json.Unmarshal(response, &record); err != nil {
		return ApplicationVerification{}, fmt.Errorf("lifecycle application verification response: %w", err)
	}
	if err := a.validateApplicationVerification(record); err != nil {
		return ApplicationVerification{}, err
	}
	return record, nil
}

func (a *LoadedLifecycleApplication) validateApplicationVerification(record ApplicationVerification) error {
	if a == nil || a.Application == nil {
		return errors.New("lifecycle application verification has no application anchor")
	}
	anchor := a.Application.Anchor
	if record.ID == "" || record.SessionID != anchor.SessionID || record.Generation != anchor.SessionGeneration || record.PluginID != a.Identity.Namespace || record.RunID == "" || record.WorkerVersion == 0 || record.Version == 0 || record.WALSequence == 0 || record.Source.EventSequence == 0 || record.Source.SessionSequence == 0 || record.Source.TurnRef == "" || record.Source.TreeDigest == "" {
		return errors.New("lifecycle application verification response has invalid broker scope")
	}
	return nil
}
