package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

// preparedSessionTransition owns every candidate resource until commit. This
// makes switching a transaction: a peer or lifecycle admission failure leaves
// the current session, controller, applications, and executor untouched.
type preparedSessionTransition struct {
	session     *stadogit.Session
	executor    *tools.Executor
	broker      runtime.BrokerController
	ownedBroker bool
	composition *lifecycleComposition
	warnings    []string
}

func (t *preparedSessionTransition) close(ctx context.Context) {
	if t == nil {
		return
	}
	if t.composition != nil {
		t.composition.close(ctx)
		t.composition = nil
	}
	if t.ownedBroker && t.broker != nil {
		_ = t.broker.Close()
	}
	t.broker = nil
}

// openSessionBroker mints a non-owning peer from the process-lifetime root
// controller when that optional transition extension is available. Controllers
// without it retain the legacy single-session behavior; they are never closed
// by the TUI because their transport ownership is unknown.
func (m *Model) openSessionBroker(ctx context.Context, session *stadogit.Session) (runtime.BrokerController, bool, error) {
	root := m.brokerRoot
	if root == nil {
		root = m.broker
	}
	if root == nil {
		return nil, false, nil
	}
	if session != nil {
		if transitioner, ok := root.(runtime.BrokerLogicalSessionTransitioner); ok {
			// The sidecar's canonical user-repository root is stable across a
			// bare first launch, `session resume`, session switching, and process
			// restart. The operator launch cwd is not: first launch commonly
			// starts inside the checkout while resume starts inside the detached
			// session worktree. Binding durable broker adoption to m.cwd would
			// therefore mint a fresh empty application scope for the same exact
			// logical subject after restart. The session worktree itself is not a
			// valid reservation cwd because it sits outside the user repository;
			// it remains the execution workdir carried by the Session.
			cwd := m.cwd
			if session.Sidecar != nil && session.Sidecar.UserRepoRoot != "" {
				cwd = session.Sidecar.UserRepoRoot
			}
			peer, err := transitioner.OpenLogicalSession(ctx, cwd, session.ID)
			if err != nil {
				return nil, false, fmt.Errorf("durable broker session transition: %w", err)
			}
			if peer == nil {
				return nil, false, errors.New("durable broker session transition: peer controller unavailable")
			}
			return peer, true, nil
		}
	}
	transitioner, ok := root.(runtime.BrokerSessionTransitioner)
	if !ok {
		return root, false, nil
	}
	peer, err := transitioner.CreatePeer(ctx, m.cwd)
	if err != nil {
		return nil, false, fmt.Errorf("broker session transition: %w", err)
	}
	if peer == nil {
		return nil, false, errors.New("broker session transition: peer controller unavailable")
	}
	return peer, true, nil
}

func (m *Model) prepareSessionTransition(ctx context.Context, sess *stadogit.Session, executor *tools.Executor) (*preparedSessionTransition, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	brokerController, ownedBroker, err := m.openSessionBroker(ctx, sess)
	if err != nil {
		return nil, err
	}
	return m.prepareSessionTransitionWithBroker(ctx, sess, executor, brokerController, ownedBroker, false)
}

// prepareSessionTransitionWithBroker stages a candidate against an already
// selected controller. Automatic recovery uses this only after the broker has
// irreversibly handed the existing durable scope to the child; ordinary
// switches continue through prepareSessionTransition and retain rollback.
func (m *Model) prepareSessionTransitionWithBroker(ctx context.Context, sess *stadogit.Session, executor *tools.Executor, brokerController runtime.BrokerController, ownedBroker, allowAdmissionFailure bool) (*preparedSessionTransition, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	prepared := &preparedSessionTransition{
		session: sess, executor: executor, broker: brokerController, ownedBroker: ownedBroker,
	}
	if executor != nil {
		m.executorSandbox.Apply(executor)
		executor.DispatchGate = runtime.SchedulingDispatchGate(brokerController)
	}

	// Application construction reads the active fields to construct its native
	// bridges. Point those reads at the candidate only for the duration of
	// staging; callbacks cannot run here because every caller passed the busy
	// gate before opening the target session.
	oldSession, oldExecutor, oldBroker := m.session, m.executor, m.broker
	oldMessages := m.msgs
	var candidateMessages []agent.Message
	if sess != nil {
		candidateMessages, _ = runtime.LoadConversation(sess.WorktreePath)
	}
	m.session, m.executor, m.broker = sess, executor, brokerController
	m.msgs = candidateMessages
	prepared.composition, prepared.warnings = m.stageLifecycleApplications(ctx, m.cfg)
	m.session, m.executor, m.broker = oldSession, oldExecutor, oldBroker
	m.msgs = oldMessages

	if prepared.composition.admissionFailure != nil {
		admissionErr := prepared.composition.admissionFailure
		if allowAdmissionFailure {
			prepared.warnings = append(prepared.warnings, "session lifecycle admission: "+admissionErr.Error())
			return prepared, nil
		}
		prepared.close(context.Background())
		return nil, fmt.Errorf("session lifecycle admission: %w", admissionErr)
	}
	return prepared, nil
}

// commitSessionTransition installs a fully prepared candidate, then retires
// the superseded application instances and non-owning broker peer. The root
// controller remains alive as the factory for later transitions and is closed
// only by the command entry point after Run returns.
func (m *Model) commitSessionTransition(ctx context.Context, prepared *preparedSessionTransition) error {
	if prepared == nil {
		return errors.New("session transition: candidate unavailable")
	}
	oldBroker, oldOwned := m.broker, m.brokerPeerOwned
	m.session = prepared.session
	m.executor = prepared.executor
	m.broker = prepared.broker
	m.brokerPeerOwned = prepared.ownedBroker
	// Worker runs are scoped to the exact broker session/generation. Never
	// carry a recurrence owner into a peer minted for another logical session.
	m.loop = nil
	m.installLifecycleComposition(ctx, prepared.composition)
	// The new controller may recover a durable application-owned recurrence.
	// Gate ordinary input synchronously; the event-loop poll resolves the
	// projection before Enter can fall through to an unsupervised provider turn.
	_, m.applicationWorkerRecoveryPending = m.broker.(runtime.ApplicationWorkerRunController)
	m.applicationFailureSources = nil
	m.applicationFailure = m.applicationAdmissionFailure
	m.applicationDeferredTaskNotice = ""
	prepared.composition = nil
	prepared.broker = nil
	prepared.ownedBroker = false
	if oldOwned && oldBroker != nil && oldBroker != m.broker {
		if err := oldBroker.Close(); err != nil {
			return fmt.Errorf("retire previous broker session: %w", err)
		}
	}
	return nil
}

func (m *Model) closeActiveBrokerPeer() error {
	if !m.brokerPeerOwned || m.broker == nil {
		return nil
	}
	peer := m.broker
	m.broker = nil
	m.brokerPeerOwned = false
	return peer.Close()
}
