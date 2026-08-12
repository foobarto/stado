// Package orchestration composes durable admission, recursive budgets,
// mailboxes, and lifecycle into retained child execution.
package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"sync"
	"time"
)

type LaunchRequest struct {
	Admission                        retained.Request
	AccountID                        string
	Budget                           brokerbudget.Limits
	Principal, Actor, IdempotencyKey string
	// Launcher is request-scoped because the admitted prompt and attenuated
	// runtime settings are not persisted as executable authority in the registry.
	Launcher      Launcher
	RestartPolicy retained.RestartPolicy
}
type LaunchResult struct {
	Usage     brokerbudget.Limits
	Transient bool
	Error     string
	FinalText string
}
type Launcher interface {
	Launch(context.Context, retained.Admission) (LaunchResult, error)
}
type Handle struct {
	AdmissionID    string          `json:"admission_id"`
	ChildSessionID string          `json:"child_session_id"`
	Generation     uint64          `json:"generation"`
	Status         retained.Status `json:"status"`
}
type Coordinator struct {
	Registry *retained.Registry
	Budgets  *brokerbudget.Ledger
	Mailbox  *mailbox.Broker
	Launcher Launcher
	LeaseTTL time.Duration
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	Policy   *mailbox.DynamicRelationPolicy
}

func New(r *retained.Registry, b *brokerbudget.Ledger, m *mailbox.Broker, l Launcher) *Coordinator {
	return &Coordinator{Registry: r, Budgets: b, Mailbox: m, Launcher: l, LeaseTTL: time.Minute, cancels: map[string]context.CancelFunc{}}
}
func (c *Coordinator) SpawnRetained(ctx context.Context, req LaunchRequest) (Handle, error) {
	launcher := req.Launcher
	if launcher == nil {
		launcher = c.Launcher
	}
	if c.Registry == nil || c.Budgets == nil || launcher == nil {
		return Handle{}, errors.New("retained coordinator is incomplete")
	}
	reservation, err := c.Budgets.Reserve(ctx, req.AccountID, req.Budget, req.Principal, req.Actor, req.IdempotencyKey+":budget")
	if err != nil {
		return Handle{}, err
	}
	req.Admission.BudgetReservationID = reservation.ID
	admission, err := c.Registry.Admit(ctx, req.Admission)
	if err != nil {
		_, _ = c.Budgets.Release(ctx, reservation.ID, req.Principal, req.Actor, req.IdempotencyKey+":release")
		return Handle{}, err
	}
	if c.Policy != nil {
		c.Policy.Allow(admission.ParentSessionID, admission.ChildSessionID)
		c.Policy.Allow(admission.ChildSessionID, admission.ParentSessionID)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancels[admission.ID] = cancel
	c.mu.Unlock()
	go c.run(runCtx, admission, req, launcher)
	return Handle{AdmissionID: admission.ID, ChildSessionID: admission.ChildSessionID, Generation: admission.Generation, Status: admission.Status}, nil
}
func (c *Coordinator) run(ctx context.Context, a retained.Admission, req LaunchRequest, launcher Launcher) {
	defer func() { c.mu.Lock(); delete(c.cancels, a.ID); c.mu.Unlock() }()
	for {
		restart, next := c.runGeneration(ctx, a, req, launcher)
		if !restart {
			return
		}
		a = next
	}
}

func (c *Coordinator) runGeneration(ctx context.Context, a retained.Admission, req LaunchRequest, launcher Launcher) (bool, retained.Admission) {
	suffix := fmt.Sprintf(":g%d", a.Generation)
	lease, err := c.Registry.AcquireLease(ctx, a.ID, a.RuntimeNonce, req.Principal, "retained-runtime", req.IdempotencyKey+":lease"+suffix, c.LeaseTTL)
	if err != nil {
		return false, a
	}
	a, err = c.Registry.Transition(ctx, a.ID, retained.StatusAdmitted, retained.StatusStarting, lease.LeaseEpoch, req.Principal, "retained-runtime", req.IdempotencyKey+":starting"+suffix)
	if err != nil {
		return false, a
	}
	a, err = c.Registry.Transition(ctx, a.ID, retained.StatusStarting, retained.StatusRunning, lease.LeaseEpoch, req.Principal, "retained-runtime", req.IdempotencyKey+":running"+suffix)
	if err != nil {
		return false, a
	}
	result, launchErr := launcher.Launch(ctx, a)
	_, _ = c.Budgets.Commit(context.Background(), a.BudgetReservationID, result.Usage, req.Principal, "provider-usage", req.IdempotencyKey+":usage"+suffix)
	status := retained.StatusCompleted
	if errors.Is(ctx.Err(), context.Canceled) {
		status = retained.StatusCancelled
	} else if launchErr != nil || result.Error != "" {
		status = retained.StatusFailed
	}
	a, _ = c.Registry.Transition(context.Background(), a.ID, retained.StatusRunning, status, lease.LeaseEpoch, req.Principal, "retained-runtime", req.IdempotencyKey+":terminal"+suffix)
	if status == retained.StatusFailed && result.Transient {
		decision, _ := c.Registry.DecideRestart(a.ID, retained.FailureTransient, req.RestartPolicy, req.Principal, "retained-supervisor", req.IdempotencyKey+":restart-decision"+suffix)
		if decision.Restart {
			_, _ = c.Registry.Transition(context.Background(), a.ID, retained.StatusFailed, retained.StatusDown, lease.LeaseEpoch, req.Principal, "retained-supervisor", req.IdempotencyKey+":down"+suffix)
			select {
			case <-ctx.Done():
				return false, a
			case <-time.After(decision.Backoff):
			}
			next, restartErr := c.Registry.RestartGeneration(context.Background(), a.ID, req.Principal, "retained-supervisor", req.IdempotencyKey+":restart"+suffix)
			if restartErr == nil {
				return true, next
			}
		}
	}
	if c.Mailbox != nil && result.FinalText != "" {
		payload, _ := json.Marshal(map[string]string{"text": result.FinalText})
		_, _ = c.Mailbox.Send(context.Background(), mailbox.SendRequest{SenderSession: a.ChildSessionID, SenderGeneration: a.Generation, ReceiverSession: a.ParentSessionID, Kind: mailbox.KindReply, CorrelationID: a.ID, Payload: payload, Principal: req.Principal, Actor: "retained-runtime", IdempotencyKey: req.IdempotencyKey + ":reply" + suffix})
	}
	return false, a
}
func (c *Coordinator) Cancel(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cancel, ok := c.cancels[id]
	if ok {
		cancel()
	}
	return ok
}
func (c *Coordinator) FollowUp(ctx context.Context, sender string, handle Handle, payload []byte, principal, actor, idem string) (mailbox.Message, error) {
	return c.Mailbox.Send(ctx, mailbox.SendRequest{SenderSession: sender, SenderGeneration: 1, ReceiverSession: handle.ChildSessionID, Kind: mailbox.KindRequest, Payload: payload, Principal: principal, Actor: actor, IdempotencyKey: idem})
}
