package tui

// Host-side execution for generic lifecycle-application verification requests.
// The application selects an anchored worker turn, never a command. Only this
// interactive native surface resolves operator-owned [verify].commands.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	maxApplicationVerificationBatch        = 32
	maxApplicationVerificationEvidenceRefs = 32
)

type applicationVerificationAnchorCheck func(runtime.ApplicationVerification) error

func serviceApplicationVerificationsSerialized(mu *sync.Mutex, ctx context.Context, applications []*runtime.LoadedLifecycleApplication, executor *tools.Executor, controller runtime.BrokerController, host tool.Host, configured runtime.VerifyConfig, checkAnchor applicationVerificationAnchorCheck) (int, error) {
	if mu == nil {
		return 0, errors.New("application verification requires a serialized native pump")
	}
	mu.Lock()
	defer mu.Unlock()
	return serviceApplicationVerifications(ctx, applications, executor, controller, host, configured, checkAnchor)
}

func serviceApplicationVerifications(ctx context.Context, applications []*runtime.LoadedLifecycleApplication, executor *tools.Executor, controller runtime.BrokerController, host tool.Host, configured runtime.VerifyConfig, checkAnchor applicationVerificationAnchorCheck) (int, error) {
	if checkAnchor == nil {
		return 0, errors.New("application verification requires an exact native session anchor check")
	}
	verificationExecutor := applicationVerificationExecutor(executor, controller)
	completed := 0
	for _, application := range applications {
		if application == nil || !manifestHasCapability(application.Manifest.Capabilities, "session:verification:request") {
			continue
		}
		for i := 0; i < maxApplicationVerificationBatch; i++ {
			record, found, err := application.NextApplicationVerification(ctx)
			if err != nil {
				return completed, fmt.Errorf("lifecycle application %s verification get: %w", application.Identity.Canonical, err)
			}
			if !found {
				break
			}
			if err := serviceApplicationVerification(ctx, application, record, verificationExecutor, host, configured, checkAnchor); err != nil {
				return completed, fmt.Errorf("lifecycle application %s verification %s: %w", application.Identity.Canonical, record.ID, err)
			}
			completed++
		}
	}
	return completed, nil
}

func applicationVerificationExecutor(executor *tools.Executor, controller runtime.BrokerController) *tools.Executor {
	if executor == nil {
		return nil
	}
	clone := *executor
	clone.DispatchGate = runtime.NativeVerificationDispatchGate(controller)
	clone.Hooks = executor.Hooks.WithoutMutations()
	return &clone
}

func serviceApplicationVerification(ctx context.Context, application *runtime.LoadedLifecycleApplication, record runtime.ApplicationVerification, executor *tools.Executor, host tool.Host, configured runtime.VerifyConfig, checkAnchor applicationVerificationAnchorCheck) error {
	commands := append([]string(nil), configured.Commands...)
	digests := applicationVerificationCommandDigests(commands)
	suiteDigest := runtime.VerificationSuiteDigest(digests)

	switch record.Status {
	case "requested":
		claimed, err := application.ClaimApplicationVerification(ctx, runtime.ApplicationVerificationClaim{
			ID: record.ID, ExpectedVersion: record.Version,
			SuiteDigest: suiteDigest, CommandDigests: digests,
		})
		if err != nil {
			return err
		}
		record = claimed
	case "running":
		if record.SuiteDigest != suiteDigest || !sameVerificationDigests(record.CommandDigests, digests) {
			return finishChangedApplicationVerification(ctx, application, record)
		}
	default:
		return fmt.Errorf("unexpected broker status %q", record.Status)
	}
	if err := checkAnchor(record); err != nil {
		return finishStaleApplicationVerification(ctx, application, record, nil, err)
	}

	if len(commands) == 0 {
		finishCtx, cancel := applicationVerificationFinishContext(ctx)
		defer cancel()
		_, err := application.FinishApplicationVerification(finishCtx, runtime.ApplicationVerificationFinish{
			ID: record.ID, ExpectedVersion: record.Version, Outcome: "no_suite",
		})
		return err
	}

	events := make([]runtime.VerifyEvent, 0, len(commands))
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var guardErr error
	outcome := runtime.RunVerificationRound(runCtx, executor, host, runtime.VerifyConfig{
		Commands: commands, MaxRounds: 1, Strict: configured.Strict,
	}, 1, func(event runtime.VerifyEvent) {
		// VerifyStarted is emitted synchronously before each Executor.Run. This
		// closes the interval between commands as well as the before/after suite
		// checks; a composition cancellation also reaches the in-flight executor.
		if event.Status == runtime.VerifyStarted {
			if err := checkAnchor(record); err != nil {
				guardErr = err
				cancelRun()
			}
			return
		}
		if event.Status != runtime.VerifyStarted {
			events = append(events, event)
		}
	})
	facts := make([]runtime.ApplicationVerificationCommandFact, len(digests))
	for i, commandDigest := range digests {
		fact := runtime.ApplicationVerificationCommandFact{
			Ordinal: i + 1, CommandDigest: commandDigest,
			ResultDigest: runtime.VerificationFactDigest(""), Outcome: "not_run",
		}
		if i < len(events) {
			event := events[i]
			fact.ResultDigest = runtime.VerificationFactDigest(event.Output)
			fact.Outcome, fact.FailureKind = applicationVerificationEventOutcome(event.Status)
			fact.EvidenceRefs = append([]string(nil), event.EvidenceRefs...)
			if fact.FailureKind != "" {
				fact.FailureFingerprint = runtime.VerificationFactDigest(string(event.Status) + "\x00" + event.Output)
			}
		}
		facts[i] = fact
	}
	if err := ctx.Err(); err != nil {
		return finishStaleApplicationVerification(ctx, application, record, facts, fmt.Errorf("native lifecycle composition ended during verification: %w", err))
	}
	if guardErr != nil {
		return finishStaleApplicationVerification(ctx, application, record, facts, guardErr)
	}
	if err := checkAnchor(record); err != nil {
		return finishStaleApplicationVerification(ctx, application, record, facts, err)
	}

	finish := runtime.ApplicationVerificationFinish{
		ID: record.ID, ExpectedVersion: record.Version,
		Outcome: applicationVerificationOutcome(outcome.Status), Commands: facts,
		EvidenceRefs: applicationVerificationEvidenceUnion(facts),
	}
	if finish.Outcome != "commands_succeeded" {
		finish.FailureKind = applicationVerificationFailureKind(outcome.Status)
		failureMaterial := string(outcome.Status) + "\x00" + outcome.Output
		if outcome.Err != nil {
			failureMaterial += "\x00" + outcome.Err.Error()
		}
		finish.FailureFingerprint = runtime.VerificationFactDigest(failureMaterial)
	}
	finishCtx, cancel := applicationVerificationFinishContext(ctx)
	defer cancel()
	_, err := application.FinishApplicationVerification(finishCtx, finish)
	return err
}

func applicationVerificationEvidenceUnion(facts []runtime.ApplicationVerificationCommandFact) []string {
	refs := make([]string, 0, len(facts)*2)
	seen := make(map[string]struct{}, len(facts)*2)
	for _, fact := range facts {
		for _, ref := range fact.EvidenceRefs {
			if ref == "" {
				continue
			}
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			refs = append(refs, ref)
			if len(refs) == maxApplicationVerificationEvidenceRefs {
				return refs
			}
		}
	}
	return refs
}

func applicationVerificationFinishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func finishStaleApplicationVerification(ctx context.Context, application *runtime.LoadedLifecycleApplication, record runtime.ApplicationVerification, facts []runtime.ApplicationVerificationCommandFact, anchorErr error) error {
	slog.Warn("lifecycle application verification source became stale", "verification", record.ID, "err", anchorErr)
	fingerprint := runtime.VerificationFactDigest("stale_anchor\x00" + anchorErr.Error())
	if facts == nil {
		facts = make([]runtime.ApplicationVerificationCommandFact, len(record.CommandDigests))
		for i, digest := range record.CommandDigests {
			facts[i] = runtime.ApplicationVerificationCommandFact{
				Ordinal: i + 1, CommandDigest: digest,
				ResultDigest: runtime.VerificationFactDigest(""), Outcome: "not_run",
			}
		}
	}
	finishCtx, cancel := applicationVerificationFinishContext(ctx)
	defer cancel()
	_, err := application.FinishApplicationVerification(finishCtx, runtime.ApplicationVerificationFinish{
		ID: record.ID, ExpectedVersion: record.Version, Outcome: "cancelled",
		FailureKind: "stale_anchor", FailureFingerprint: fingerprint, Commands: facts,
	})
	return err
}

func applicationVerificationAnchorForSession(session *stadogit.Session) applicationVerificationAnchorCheck {
	return func(record runtime.ApplicationVerification) error {
		if session == nil || session.Sidecar == nil {
			return errors.New("verification source has no active native Git session")
		}
		version := record.Source.TreeDigest
		expectedTree := plumbing.ZeroHash
		if version != "empty" {
			if !plumbing.IsHash(version) || strings.ToLower(version) != version {
				return errors.New("verification source tree commit is invalid")
			}
			var err error
			expectedTree, err = session.TreeFromCommit(plumbing.NewHash(version))
			if err != nil {
				return fmt.Errorf("resolve verification source tree commit: %w", err)
			}
		}
		currentTree, err := session.CurrentTree()
		if err != nil {
			return fmt.Errorf("read current session content tree: %w", err)
		}
		if currentTree != expectedTree {
			changed, changeErr := session.ChangedFilesBetween(expectedTree, currentTree)
			if changeErr != nil {
				return fmt.Errorf("verification source content tree changed (%s -> %s); inspect diff: %w", expectedTree, currentTree, changeErr)
			}
			if len(changed) > 8 {
				changed = changed[:8]
			}
			return fmt.Errorf("verification source content tree changed (%s -> %s): %s", expectedTree, currentTree, strings.Join(changed, ", "))
		}
		prefix := "git:" + stadogit.TreeRef(session.ID).String() + "@" + version + "#"
		if !strings.HasPrefix(record.Source.TurnRef, prefix) {
			return errors.New("verification turn ref does not name the current session tree")
		}
		fragment := strings.TrimPrefix(record.Source.TurnRef, prefix)
		var turn, iteration int
		if _, err := fmt.Sscanf(fragment, "turn-%d-iteration-%d", &turn, &iteration); err != nil || turn < 1 || iteration < 1 || fragment != fmt.Sprintf("turn-%d-iteration-%d", turn, iteration) {
			return errors.New("verification turn ref has an invalid coordinate")
		}
		return nil
	}
}

func finishChangedApplicationVerification(ctx context.Context, application *runtime.LoadedLifecycleApplication, record runtime.ApplicationVerification) error {
	facts := make([]runtime.ApplicationVerificationCommandFact, len(record.CommandDigests))
	fingerprint := runtime.VerificationFactDigest("verification suite changed before crash recovery")
	for i, digest := range record.CommandDigests {
		facts[i] = runtime.ApplicationVerificationCommandFact{
			Ordinal: i + 1, CommandDigest: digest, ResultDigest: runtime.VerificationFactDigest(""),
			Outcome: "not_run",
		}
	}
	finishCtx, cancel := applicationVerificationFinishContext(ctx)
	defer cancel()
	_, err := application.FinishApplicationVerification(finishCtx, runtime.ApplicationVerificationFinish{
		ID: record.ID, ExpectedVersion: record.Version, Outcome: "infrastructure_error",
		FailureKind: "suite_changed", FailureFingerprint: fingerprint, Commands: facts,
	})
	return err
}

func applicationVerificationCommandDigests(commands []string) []string {
	digests := make([]string, len(commands))
	for i, command := range commands {
		digests[i] = runtime.VerificationFactDigest(command)
	}
	return digests
}

func applicationVerificationOutcome(status runtime.VerifyStatus) string {
	switch status {
	case runtime.VerifyPassed:
		return "commands_succeeded"
	case runtime.VerifyFailed:
		return "command_failed"
	case runtime.VerifyCancelled:
		return "cancelled"
	default:
		return "infrastructure_error"
	}
}

func applicationVerificationFailureKind(status runtime.VerifyStatus) string {
	switch status {
	case runtime.VerifyFailed:
		return "command_failed"
	case runtime.VerifyCancelled:
		return "cancelled"
	default:
		return "infrastructure_error"
	}
}

func applicationVerificationEventOutcome(status runtime.VerifyStatus) (string, string) {
	switch status {
	case runtime.VerifyPassed:
		return "succeeded", ""
	case runtime.VerifyFailed:
		return "failed", "command_failed"
	case runtime.VerifyCancelled:
		return "cancelled", "cancelled"
	default:
		return "infrastructure_error", "infrastructure_error"
	}
}

func sameVerificationDigests(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func manifestHasCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if strings.TrimSpace(capability) == want {
			return true
		}
	}
	return false
}
