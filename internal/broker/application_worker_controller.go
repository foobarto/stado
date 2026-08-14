package broker

// Native controller RPC for lifecycle-application worker ownership. The
// application bearer selects an already broker-admitted plugin namespace; the
// independent session-controller bearer authorizes the native transition.
// These operations are intentionally absent from applicationOperations and
// therefore cannot be reached through any WASM host import.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/broker/application"
)

type ApplicationWorkerGetParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	BindingToken    string `json:"binding_token"`
	RunID           string `json:"run_id"`
}

type ApplicationWorkerTransitionParams struct {
	SessionID       string `json:"session_id"`
	ControllerToken string `json:"controller_token"`
	BindingToken    string `json:"binding_token"`
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

func (s *Service) applicationWorkerBinding(sessionID, controllerToken, bindingToken string, capabilities ...string) (artifactBinding, error) {
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
	for _, capability := range capabilities {
		if binding.hasCapability(capability) {
			return binding, nil
		}
	}
	return artifactBinding{}, fmt.Errorf("none of the application worker capabilities %q are admitted", capabilities)
}

func (s *Service) applicationWorkerGet(ctx context.Context, params ApplicationWorkerGetParams) (application.WorkerRun, error) {
	binding, err := s.applicationWorkerBinding(params.SessionID, params.ControllerToken, params.BindingToken, "session:worker:request", "session:worker:resume")
	if err != nil {
		return application.WorkerRun{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.WorkerRun{}, errors.New("application state authority unavailable")
	}
	return s.artifacts.application.WorkerRunByID(ctx, binding.applicationAuthority(), params.RunID)
}

func (s *Service) applicationWorkerActivate(ctx context.Context, params ApplicationWorkerTransitionParams) (application.WorkerRun, error) {
	binding, err := s.applicationWorkerBinding(params.SessionID, params.ControllerToken, params.BindingToken, "session:worker:request")
	if err != nil {
		return application.WorkerRun{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.WorkerRun{}, errors.New("application state authority unavailable")
	}
	input := application.WorkerRunCAS{RunID: params.RunID, ExpectedVersion: params.ExpectedVersion}
	return s.artifacts.application.ActivateWorkerRun(ctx, binding.applicationAuthority(), input,
		fmt.Sprintf("controller:worker:activate:%s:%d", params.RunID, params.ExpectedVersion))
}

func (s *Service) applicationWorkerResumeActivate(ctx context.Context, params ApplicationWorkerTransitionParams) (application.WorkerRun, error) {
	binding, err := s.applicationWorkerBinding(params.SessionID, params.ControllerToken, params.BindingToken, "session:worker:resume")
	if err != nil {
		return application.WorkerRun{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.WorkerRun{}, errors.New("application state authority unavailable")
	}
	input := application.WorkerRunCAS{RunID: params.RunID, ExpectedVersion: params.ExpectedVersion}
	return s.artifacts.application.ActivateResumedWorkerRun(ctx, binding.applicationAuthority(), input,
		fmt.Sprintf("controller:worker:resume:activate:%s:%d", params.RunID, params.ExpectedVersion))
}

func (s *Service) applicationWorkerCancel(ctx context.Context, params ApplicationWorkerTransitionParams) (application.WorkerRun, error) {
	binding, err := s.applicationWorkerBinding(params.SessionID, params.ControllerToken, params.BindingToken, "session:worker:request", "session:worker:resume")
	if err != nil {
		return application.WorkerRun{}, err
	}
	if s.artifacts == nil || s.artifacts.application == nil {
		return application.WorkerRun{}, errors.New("application state authority unavailable")
	}
	input := application.WorkerRunCAS{RunID: params.RunID, ExpectedVersion: params.ExpectedVersion, Reason: params.Reason}
	return s.artifacts.application.CancelWorkerRun(ctx, binding.applicationAuthority(), input,
		fmt.Sprintf("controller:worker:cancel:%s:%d", params.RunID, params.ExpectedVersion))
}
