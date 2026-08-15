package broker

// Native controller RPC for generic asynchronous verification. The guest can
// create a request through its fixed host import; only the independently
// authenticated session controller may inspect, claim, or finish that request.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/broker/application"
)

type ApplicationVerificationGetParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	BindingToken    string `json:"binding_token"`
	ID              string `json:"id,omitempty"`
}

type ApplicationVerificationGetResult struct {
	Found        bool                     `json:"found"`
	Verification application.Verification `json:"verification,omitempty"`
}

type ApplicationVerificationClaimParams struct {
	SessionID       string   `json:"session_id"`
	ControllerToken string   `json:"controller_token"`
	BindingToken    string   `json:"binding_token"`
	ID              string   `json:"id"`
	ExpectedVersion uint64   `json:"expected_version"`
	SuiteDigest     string   `json:"suite_digest"`
	CommandDigests  []string `json:"command_digests"`
}

type ApplicationVerificationFinishParams struct {
	SessionID          string                                `json:"session_id"`
	ControllerToken    string                                `json:"controller_token"`
	BindingToken       string                                `json:"binding_token"`
	ID                 string                                `json:"id"`
	ExpectedVersion    uint64                                `json:"expected_version"`
	Outcome            application.VerificationOutcome       `json:"outcome"`
	FailureKind        string                                `json:"failure_kind,omitempty"`
	FailureFingerprint string                                `json:"failure_fingerprint,omitempty"`
	Commands           []application.VerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                              `json:"evidence_refs,omitempty"`
}

func (s *Service) applicationVerificationBinding(sessionID, controllerToken, bindingToken string) (artifactBinding, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(controllerToken) == "" || strings.TrimSpace(bindingToken) == "" {
		return artifactBinding{}, errors.New("session_id, controller_token, and binding_token are required")
	}
	if err := s.authenticateSessionController(sessionID, controllerToken); err != nil {
		return artifactBinding{}, err
	}
	binding, err := s.applicationBinding(bindingToken)
	if err != nil {
		return artifactBinding{}, err
	}
	if binding.sessionID != sessionID {
		return artifactBinding{}, errors.New("application binding belongs to another session")
	}
	if !binding.hasCapability("session:verification:request") {
		return artifactBinding{}, errors.New("application capability \"session:verification:request\" is not admitted")
	}
	return binding, nil
}

func (s *Service) applicationVerificationGet(ctx context.Context, params ApplicationVerificationGetParams) (ApplicationVerificationGetResult, error) {
	binding, err := s.applicationVerificationBinding(params.SessionID, params.ControllerToken, params.BindingToken)
	if err != nil {
		return ApplicationVerificationGetResult{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return ApplicationVerificationGetResult{}, errors.New("application state authority unavailable")
	}
	var record application.Verification
	if strings.TrimSpace(params.ID) == "" {
		record, err = s.artifacts.application.NextVerification(ctx, binding.applicationAuthority())
	} else {
		record, err = s.artifacts.application.VerificationByID(ctx, binding.applicationAuthority(), params.ID)
	}
	if errors.Is(err, application.ErrNotFound) {
		return ApplicationVerificationGetResult{Found: false}, nil
	}
	if err != nil {
		return ApplicationVerificationGetResult{}, err
	}
	return ApplicationVerificationGetResult{Found: true, Verification: record}, nil
}

func (s *Service) applicationVerificationClaim(ctx context.Context, params ApplicationVerificationClaimParams) (application.Verification, error) {
	binding, err := s.applicationVerificationBinding(params.SessionID, params.ControllerToken, params.BindingToken)
	if err != nil {
		return application.Verification{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.Verification{}, errors.New("application state authority unavailable")
	}
	input := application.VerificationClaim{
		ID: params.ID, ExpectedVersion: params.ExpectedVersion,
		SuiteDigest: params.SuiteDigest, CommandDigests: params.CommandDigests,
	}
	return s.artifacts.application.ClaimVerification(ctx, binding.applicationAuthority(), input,
		fmt.Sprintf("controller:verification:claim:%s:%d", params.ID, params.ExpectedVersion))
}

func (s *Service) applicationVerificationFinish(ctx context.Context, params ApplicationVerificationFinishParams) (application.Verification, error) {
	binding, err := s.applicationVerificationBinding(params.SessionID, params.ControllerToken, params.BindingToken)
	if err != nil {
		return application.Verification{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.Verification{}, errors.New("application state authority unavailable")
	}
	input := application.VerificationFinish{
		ID: params.ID, ExpectedVersion: params.ExpectedVersion, Outcome: params.Outcome,
		FailureKind: params.FailureKind, FailureFingerprint: params.FailureFingerprint,
		Commands: params.Commands, EvidenceRefs: params.EvidenceRefs,
	}
	return s.artifacts.application.FinishVerification(ctx, binding.applicationAuthority(), input,
		fmt.Sprintf("controller:verification:finish:%s:%d", params.ID, params.ExpectedVersion))
}
